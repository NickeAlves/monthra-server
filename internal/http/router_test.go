package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/csrf"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/observability"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
)

const testOrigin = "https://app.monthra.example"

// memLimiter is an in-process fixed budget per key, enough to drive the router.
type memLimiter struct {
	mu   sync.Mutex
	used map[string]int
}

func (m *memLimiter) Allow(_ context.Context, key string, rate config.Rate) (ratelimit.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used == nil {
		m.used = map[string]int{}
	}
	if m.used[key] >= rate.Limit {
		return ratelimit.Result{Limit: rate.Limit, RetryAfter: rate.Period, ResetAfter: rate.Period}, nil
	}
	m.used[key]++
	return ratelimit.Result{Allowed: true, Limit: rate.Limit, Remaining: rate.Limit - m.used[key], ResetAfter: rate.Period}, nil
}

func testSigner(t *testing.T) csrf.Signer {
	t.Helper()
	s, err := csrf.NewSigner(bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	return s
}

func newTestRouterWith(t *testing.T, serveDocs bool, apiRate config.Rate) http.Handler {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	return NewRouter(RouterOptions{
		Logger:  logger,
		Metrics: observability.NewMetrics("test", "local"),
		Health: handler.NewHealth(logger, time.Second, map[string]handler.Pinger{
			"mongo": handler.PingerFunc(func(context.Context) error { return nil }),
		}),
		MaxBodyBytes: 16,
		HSTS:         true,
		ServeDocs:    serveDocs,

		AllowedOrigins: []string{testOrigin},
		Cookies:        cookie.Policy{Secure: true},
		CSRFSigner:     testSigner(t),
		Limiter:        &memLimiter{},
		Keys:           cache.NewKeys(config.EnvLocal),
		APIRateByIP:    apiRate,
	})
}

func newTestRouter(t *testing.T, serveDocs bool) http.Handler {
	t.Helper()
	return newTestRouterWith(t, serveDocs, config.Rate{Limit: 1000, Period: time.Minute})
}

func do(h http.Handler, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHealthRoutes(t *testing.T) {
	h := newTestRouter(t, false)

	live := do(h, http.MethodGet, "/healthz", "", nil)
	assert.Equal(t, http.StatusOK, live.Code)
	assert.NotEmpty(t, live.Header().Get("X-Request-ID"))
	assert.Equal(t, "nosniff", live.Header().Get("X-Content-Type-Options"))
	assert.NotEmpty(t, live.Header().Get("Strict-Transport-Security"))

	ready := do(h, http.MethodGet, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, ready.Code)

	head := do(h, http.MethodHead, "/healthz", "", nil)
	assert.Equal(t, http.StatusOK, head.Code, "HEAD falls back to the GET route")
}

func TestNotFoundIsTranslatedProblem(t *testing.T) {
	h := newTestRouter(t, false)

	for _, path := range []string{"/nope", "/api/v1/nope"} {
		w := do(h, http.MethodGet, path, "", map[string]string{"Accept-Language": "es-ES"})

		assert.Equal(t, http.StatusNotFound, w.Code, path)
		assert.Equal(t, problem.ContentType, w.Header().Get("Content-Type"))
		var p problem.Problem
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
		assert.Equal(t, "No encontrado", p.Title)
		assert.Equal(t, "not_found", p.Code)
		assert.Equal(t, w.Header().Get("X-Request-ID"), p.RequestID)
	}
}

func TestMethodNotAllowedIsProblem(t *testing.T) {
	w := do(newTestRouter(t, false), http.MethodPost, "/healthz", "", map[string]string{"Accept-Language": "en-US"})

	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	assert.Contains(t, w.Body.String(), `"title":"Method not allowed"`)
}

func TestBodyLimitApplies(t *testing.T) {
	w := do(newTestRouter(t, false), http.MethodPost, "/api/v1/anything", strings.Repeat("x", 17), nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestDocsOnlyWhenEnabled(t *testing.T) {
	on := do(newTestRouter(t, true), http.MethodGet, "/docs/openapi.yaml", "", nil)
	assert.Equal(t, http.StatusOK, on.Code)
	assert.Equal(t, "application/yaml", on.Header().Get("Content-Type"))
	assert.True(t, bytes.HasPrefix(on.Body.Bytes(), []byte("openapi: 3.1")))

	off := do(newTestRouter(t, false), http.MethodGet, "/docs/openapi.yaml", "", nil)
	assert.Equal(t, http.StatusNotFound, off.Code)
}

func TestNewServerTimeouts(t *testing.T) {
	cfg := config.HTTP{ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 4 * time.Second}
	srv := NewServer(":0", http.NotFoundHandler(), cfg, slog.New(slog.DiscardHandler))

	assert.Equal(t, time.Second, srv.ReadHeaderTimeout)
	assert.Equal(t, 2*time.Second, srv.ReadTimeout)
	assert.Equal(t, 3*time.Second, srv.WriteTimeout)
	assert.Equal(t, 4*time.Second, srv.IdleTimeout)
	assert.Equal(t, maxHeaderBytes, srv.MaxHeaderBytes)
	assert.NotNil(t, srv.ErrorLog)
}

func TestMetricsHandlerServesOnlyMetrics(t *testing.T) {
	m := observability.NewMetrics("test", "local")
	h := NewMetricsHandler(m.Handler())

	assert.Equal(t, http.StatusOK, do(h, http.MethodGet, "/metrics", "", nil).Code)
	assert.Equal(t, http.StatusNotFound, do(h, http.MethodGet, "/healthz", "", nil).Code)
}

func TestCSRFRouteAndGuardWork(t *testing.T) {
	h := newTestRouter(t, false)

	issued := do(h, http.MethodGet, "/api/v1/auth/csrf", "", map[string]string{"Origin": testOrigin})
	require.Equal(t, http.StatusOK, issued.Code)
	assert.Equal(t, testOrigin, issued.Header().Get("Access-Control-Allow-Origin"))
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	require.NoError(t, json.Unmarshal(issued.Body.Bytes(), &body))
	require.NotEmpty(t, body.CSRFToken)

	// Browser POST without the token: rejected before reaching any route.
	denied := do(h, http.MethodPost, "/api/v1/anything", "", map[string]string{"Origin": testOrigin})
	assert.Equal(t, http.StatusForbidden, denied.Code)
	assert.Contains(t, denied.Body.String(), `"code":"csrf_invalid"`)
	assert.Equal(t, testOrigin, denied.Header().Get("Access-Control-Allow-Origin"), "the page can read the 403")

	// With the token in cookie and header it goes through (to the 404 of an unknown route).
	r := httptest.NewRequest(http.MethodPost, "/api/v1/anything", nil)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("X-CSRF-Token", body.CSRFToken)
	r.AddCookie(&http.Cookie{Name: "__Host-monthra_csrf", Value: body.CSRFToken})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assert.Equal(t, http.StatusNotFound, w.Code)

	evil := do(h, http.MethodPost, "/api/v1/anything", "", map[string]string{"Origin": "https://evil.example"})
	assert.Equal(t, http.StatusForbidden, evil.Code)
	assert.Contains(t, evil.Body.String(), `"code":"origin_not_allowed"`)
}

func TestPreflightIsAnsweredBeforeRouting(t *testing.T) {
	h := newTestRouter(t, false)
	w := do(h, http.MethodOptions, "/api/v1/auth/login", "", map[string]string{
		"Origin":                        testOrigin,
		"Access-Control-Request-Method": "POST",
	})
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
}

func TestAPIRateLimitByIP(t *testing.T) {
	h := newTestRouterWith(t, false, config.Rate{Limit: 2, Period: time.Minute})
	for i := range 2 {
		w := do(h, http.MethodGet, "/api/v1/auth/csrf", "", nil)
		require.Equal(t, http.StatusOK, w.Code, "request %d", i+1)
		assert.Equal(t, "2", w.Header().Get("RateLimit-Limit"))
	}
	w := do(h, http.MethodGet, "/api/v1/auth/csrf", "", map[string]string{"Origin": testOrigin})
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "60", w.Header().Get("Retry-After"))
	assert.Equal(t, testOrigin, w.Header().Get("Access-Control-Allow-Origin"), "the page can read the 429")

	health := do(h, http.MethodGet, "/healthz", "", nil)
	assert.Equal(t, http.StatusOK, health.Code, "probes are outside /api/v1 and its budget")
	assert.Empty(t, health.Header().Get("RateLimit-Limit"))
}
