package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/NickeAlves/monthra-server/internal/http/problem"
)

func okPinger() Pinger { return PingerFunc(func(context.Context) error { return nil }) }

func TestLiveIgnoresDependencies(t *testing.T) {
	h := NewHealth(slog.New(slog.DiscardHandler), time.Second, map[string]Pinger{
		"mongo": PingerFunc(func(context.Context) error { return errors.New("down") }),
	})
	w := httptest.NewRecorder()
	h.Live(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
}

func TestReadyAllHealthy(t *testing.T) {
	h := NewHealth(slog.New(slog.DiscardHandler), time.Second, map[string]Pinger{
		"mongo": okPinger(), "redis": okPinger(),
	})
	w := httptest.NewRecorder()
	h.Ready(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
}

func TestReadyFailsWhenAnyDependencyFails(t *testing.T) {
	h := NewHealth(slog.New(slog.DiscardHandler), time.Second, map[string]Pinger{
		"mongo": okPinger(),
		"redis": PingerFunc(func(context.Context) error { return errors.New("connection refused 10.0.0.5:6379") }),
	})
	w := httptest.NewRecorder()
	h.Ready(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, problem.ContentType, w.Header().Get("Content-Type"))
	assert.NotContains(t, w.Body.String(), "10.0.0.5", "internal errors are not exposed")
	assert.Contains(t, w.Body.String(), `"code":"service_unavailable"`)
}

func TestReadyTimesOutSlowDependencies(t *testing.T) {
	h := NewHealth(slog.New(slog.DiscardHandler), 50*time.Millisecond, map[string]Pinger{
		"mongo": PingerFunc(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}),
	})
	start := time.Now()
	w := httptest.NewRecorder()
	h.Ready(w, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Less(t, time.Since(start), time.Second)
}
