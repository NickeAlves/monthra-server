package email

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

type fakeSender struct {
	mu      sync.Mutex
	sent    []Email
	fail    int // fail the first n attempts
	calls   int
	block   chan struct{}
	started chan struct{}
}

func (f *fakeSender) Send(ctx context.Context, e Email) error {
	f.mu.Lock()
	f.calls++
	call := f.calls
	block, started := f.block, f.started
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if call <= f.fail {
		return errors.New("smtp: 451 try again later")
	}
	f.mu.Lock()
	f.sent = append(f.sent, e)
	f.mu.Unlock()
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type fakeMetrics struct {
	mu      sync.Mutex
	results map[string]int
	dropped int
}

func (m *fakeMetrics) EmailSent(template, result string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.results == nil {
		m.results = map[string]int{}
	}
	m.results[template+"/"+result]++
}

func (m *fakeMetrics) EmailDropped(string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped++
}

func (m *fakeMetrics) get(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.results[key]
}

func (m *fakeMetrics) drops() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped
}

var verifyMsg = Message{To: "ana@example.com", Locale: i18n.EnUS, Template: TemplateVerifyEmail, Link: "https://x.example/v", LinkTTL: time.Minute}

func newDispatcher(t *testing.T, s Sender, m Metrics, o DispatcherOptions) *Dispatcher {
	t.Helper()
	o.Sender, o.Metrics, o.Renderer, o.Logger = s, m, newRenderer(t), slog.New(slog.DiscardHandler)
	if o.Backoff == 0 {
		o.Backoff = time.Millisecond
	}
	return NewDispatcher(o)
}

func TestDispatcherSendsAndDrains(t *testing.T) {
	s, m := &fakeSender{}, &fakeMetrics{}
	d := newDispatcher(t, s, m, DispatcherOptions{})
	for range 5 {
		d.Send(context.Background(), verifyMsg)
	}
	require.NoError(t, d.Close(context.Background()))
	assert.Equal(t, 5, s.count())
	assert.Equal(t, 5, m.get("verify_email/sent"))
	assert.Equal(t, "Confirm your email for Monthra", s.sent[0].Subject)

	d.Send(context.Background(), verifyMsg)
	assert.Equal(t, 1, m.drops(), "sending after Close drops")
	require.NoError(t, d.Close(context.Background()), "Close is idempotent")
}

func TestDispatcherRetriesTransientFailures(t *testing.T) {
	s, m := &fakeSender{fail: 2}, &fakeMetrics{}
	d := newDispatcher(t, s, m, DispatcherOptions{Workers: 1, Attempts: 3})
	d.Send(context.Background(), verifyMsg)
	require.NoError(t, d.Close(context.Background()))
	assert.Equal(t, 1, s.count())
	assert.Equal(t, 3, s.calls)
	assert.Equal(t, 1, m.get("verify_email/sent"))
}

func TestDispatcherGivesUp(t *testing.T) {
	s, m := &fakeSender{fail: 100}, &fakeMetrics{}
	d := newDispatcher(t, s, m, DispatcherOptions{Workers: 1, Attempts: 2})
	d.Send(context.Background(), verifyMsg)
	require.NoError(t, d.Close(context.Background()))
	assert.Equal(t, 0, s.count())
	assert.Equal(t, 2, s.calls)
	assert.Equal(t, 1, m.get("verify_email/failed"))
}

func TestDispatcherDropsWhenTheQueueIsFull(t *testing.T) {
	s := &fakeSender{block: make(chan struct{}), started: make(chan struct{}, 10)}
	m := &fakeMetrics{}
	d := newDispatcher(t, s, m, DispatcherOptions{Workers: 1, QueueSize: 1})

	d.Send(context.Background(), verifyMsg) // taken by the worker, which blocks
	<-s.started
	d.Send(context.Background(), verifyMsg) // waits in the queue
	d.Send(context.Background(), verifyMsg) // no room: dropped
	assert.Equal(t, 1, m.drops())

	close(s.block)
	require.NoError(t, d.Close(context.Background()))
	assert.Equal(t, 2, s.count())
}

func TestDispatcherCloseGivesUpAfterTheDeadline(t *testing.T) {
	s := &fakeSender{block: make(chan struct{}), started: make(chan struct{}, 10)}
	m := &fakeMetrics{}
	d := newDispatcher(t, s, m, DispatcherOptions{Workers: 1, Attempts: 5, Backoff: time.Hour})
	d.Send(context.Background(), verifyMsg)
	<-s.started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := d.Close(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Eventually(t, func() bool { return m.get("verify_email/failed") == 1 }, time.Second, 5*time.Millisecond,
		"the in-flight send is cancelled and not retried")
}

func TestDispatcherCountsRenderErrors(t *testing.T) {
	s, m := &fakeSender{}, &fakeMetrics{}
	d := newDispatcher(t, s, m, DispatcherOptions{})
	d.Send(context.Background(), Message{Template: "nope"})
	require.NoError(t, d.Close(context.Background()))
	assert.Equal(t, 1, m.get("nope/render_error"))
	assert.Equal(t, 0, s.calls)
}
