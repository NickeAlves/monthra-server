package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/observability"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
)

type fakeLimiter struct {
	result ratelimit.Result
	err    error
	keys   []string
}

func (f *fakeLimiter) Allow(_ context.Context, key string, _ config.Rate) (ratelimit.Result, error) {
	f.keys = append(f.keys, key)
	return f.result, f.err
}

func rateLimitDo(t *testing.T, l ratelimit.Limiter, failOpen bool) (*httptest.ResponseRecorder, bool, *observability.Metrics) {
	t.Helper()
	metrics := observability.NewMetrics("test", "local")
	called := false
	h := RateLimit(RateLimitOptions{
		Limiter: l,
		Keys:    cache.NewKeys(config.EnvLocal),
		Policy:  ratelimit.Policy{Name: "test", Rate: config.Rate{Limit: 10, Period: time.Minute}, FailOpen: failOpen},
		Subject: func(*http.Request) string { return "subject" },
		Logger:  slog.New(slog.DiscardHandler),
		Metrics: metrics,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	return w, called, metrics
}

func metricsBody(t *testing.T, m *observability.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestRateLimitAllows(t *testing.T) {
	l := &fakeLimiter{result: ratelimit.Result{Allowed: true, Limit: 10, Remaining: 7, ResetAfter: 1500 * time.Millisecond}}
	w, called, _ := rateLimitDo(t, l, false)

	assert.True(t, called)
	assert.Equal(t, []string{"monthra:local:rl:test:subject"}, l.keys)
	assert.Equal(t, "10", w.Header().Get("RateLimit-Limit"))
	assert.Equal(t, "7", w.Header().Get("RateLimit-Remaining"))
	assert.Equal(t, "2", w.Header().Get("RateLimit-Reset"), "rounded up")
	assert.Empty(t, w.Header().Get("Retry-After"))
}

func TestRateLimitRejects(t *testing.T) {
	l := &fakeLimiter{result: ratelimit.Result{Limit: 10, RetryAfter: 200 * time.Millisecond, ResetAfter: time.Minute}}
	w, called, metrics := rateLimitDo(t, l, false)

	assert.False(t, called)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "1", w.Header().Get("Retry-After"), "never below one second")
	assert.Equal(t, "0", w.Header().Get("RateLimit-Remaining"))
	assert.Equal(t, problem.ContentType, w.Header().Get("Content-Type"))
	var p problem.Problem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
	assert.Equal(t, "rate_limited", p.Code)
	assert.Contains(t, metricsBody(t, metrics), `monthra_ratelimit_rejected_total{policy="test"} 1`)
}

func TestRateLimitRedisDown(t *testing.T) {
	down := &fakeLimiter{err: errors.New("connection refused")}

	w, called, metrics := rateLimitDo(t, down, true)
	assert.True(t, called, "fail-open policies let the request through")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, metricsBody(t, metrics), `monthra_ratelimit_errors_total{policy="test"} 1`)

	w, called, _ = rateLimitDo(t, down, false)
	assert.False(t, called, "fail-closed policies stop it")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
