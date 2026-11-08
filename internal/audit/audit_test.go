package audit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memStore struct {
	entries []Entry
	err     error
	ctxErr  error
}

func (m *memStore) Insert(ctx context.Context, e Entry) error {
	m.ctxErr = ctx.Err()
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, e)
	return nil
}

func TestRecorderStampsAndTruncates(t *testing.T) {
	store := &memStore{}
	r := NewRecorder(store, slog.New(slog.DiscardHandler))
	r.now = func() time.Time { return time.Date(2026, 11, 8, 9, 0, 0, 0, time.FixedZone("BRT", -3*3600)) }

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the request may be gone already; the entry is still written
	r.Record(ctx, Entry{Event: EventLoginSuccess, UserID: "u1", UserAgent: strings.Repeat("é", 200)})

	require.Len(t, store.entries, 1)
	e := store.entries[0]
	assert.NoError(t, store.ctxErr)
	assert.Equal(t, time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC), e.CreatedAt)
	assert.LessOrEqual(t, len(e.UserAgent), maxUserAgent)
	assert.Equal(t, strings.Repeat("é", maxUserAgent/2), e.UserAgent, "cut on a rune boundary")
}

func TestRecorderLogsFailures(t *testing.T) {
	var buf bytes.Buffer
	r := NewRecorder(&memStore{err: errors.New("mongo down")}, slog.New(slog.NewJSONHandler(&buf, nil)))
	r.Record(context.Background(), Entry{Event: EventLogout})
	assert.Contains(t, buf.String(), `"event":"logout"`)
	assert.Contains(t, buf.String(), "mongo down")
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "abc", truncate("abc", 5))
	assert.Equal(t, "ab", truncate("abc", 2))
	assert.Equal(t, "a", truncate("aé", 2))
}
