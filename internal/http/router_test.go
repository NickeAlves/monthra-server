package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

func newTestRouter(t *testing.T, serveDocs bool) http.Handler {
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
	})
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
