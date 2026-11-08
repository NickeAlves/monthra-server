package email

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Metrics counts delivery outcomes.
type Metrics interface {
	EmailSent(template, result string)
	EmailDropped(template string)
}

// DispatcherOptions configures a Dispatcher. Zero values get defaults.
type DispatcherOptions struct {
	Sender   Sender
	Renderer *Renderer
	Logger   *slog.Logger
	Metrics  Metrics
	// QueueSize bounds the emails waiting to be sent (default 256).
	QueueSize int
	// Workers is the number of concurrent deliveries (default 2).
	Workers int
	// Attempts per email, including the first (default 3).
	Attempts int
	// Backoff is the wait before the second attempt; it doubles after (default 2s).
	Backoff time.Duration
	// SendTimeout bounds one attempt (default 30s).
	SendTimeout time.Duration
}

// Dispatcher queues emails and delivers them in the background, retrying
// transient failures. It never blocks the caller: when the queue is full the
// email is dropped, counted and logged.
type Dispatcher struct {
	o      DispatcherOptions
	queue  chan Email
	wg     sync.WaitGroup
	mu     sync.RWMutex
	closed bool
	// stop interrupts backoff waits once Close gives up waiting.
	stop     context.Context
	stopFunc context.CancelFunc
}

// NewDispatcher starts the workers.
func NewDispatcher(o DispatcherOptions) *Dispatcher {
	if o.QueueSize <= 0 {
		o.QueueSize = 256
	}
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.Attempts <= 0 {
		o.Attempts = 3
	}
	if o.Backoff <= 0 {
		o.Backoff = 2 * time.Second
	}
	if o.SendTimeout <= 0 {
		o.SendTimeout = 30 * time.Second
	}
	o.Logger = o.Logger.With(slog.String("component", "email"))
	stop, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{o: o, queue: make(chan Email, o.QueueSize), stop: stop, stopFunc: cancel}
	for range o.Workers {
		d.wg.Go(d.work)
	}
	return d
}

// Send renders m and queues it. Rendering errors are bugs and are logged;
// delivery happens later and its outcome is only logged and counted.
func (d *Dispatcher) Send(ctx context.Context, m Message) {
	e, err := d.o.Renderer.Render(m)
	if err != nil {
		d.o.Logger.ErrorContext(ctx, "render email", slog.String("template", string(m.Template)), slog.String("error", err.Error()))
		d.o.Metrics.EmailSent(string(m.Template), "render_error")
		return
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		d.drop(ctx, e, "dispatcher closed")
		return
	}
	select {
	case d.queue <- e:
	default:
		d.drop(ctx, e, "queue full")
	}
}

func (d *Dispatcher) drop(ctx context.Context, e Email, reason string) {
	d.o.Metrics.EmailDropped(string(e.Template))
	d.o.Logger.ErrorContext(ctx, "email dropped", slog.String("template", string(e.Template)), slog.String("reason", reason))
}

// Close stops accepting emails and waits until the queued ones are sent or
// ctx ends.
func (d *Dispatcher) Close(ctx context.Context) error {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.queue)
	}
	d.mu.Unlock()

	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		d.stopFunc()
		return fmt.Errorf("email queue not drained (%d left): %w", len(d.queue), ctx.Err())
	}
}

func (d *Dispatcher) work() {
	for e := range d.queue {
		d.deliver(e)
	}
}

func (d *Dispatcher) deliver(e Email) {
	wait := d.o.Backoff
	var err error
	for attempt := 1; attempt <= d.o.Attempts; attempt++ {
		ctx, cancel := context.WithTimeout(d.stop, d.o.SendTimeout)
		err = d.o.Sender.Send(ctx, e)
		cancel()
		if err == nil {
			d.o.Metrics.EmailSent(string(e.Template), "sent")
			return
		}
		if attempt == d.o.Attempts || d.stop.Err() != nil {
			break
		}
		select {
		case <-time.After(wait):
			wait *= 2
		case <-d.stop.Done():
		}
	}
	d.o.Metrics.EmailSent(string(e.Template), "failed")
	// The recipient and the link are personal data and secrets: never logged.
	d.o.Logger.Error("email delivery failed", slog.String("template", string(e.Template)), slog.String("error", err.Error()))
}
