package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

var ok = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func TestRequestIDGeneratesWhenMissingOrInvalid(t *testing.T) {
	for name, incoming := range map[string]string{
		"missing":   "",
		"too short": "abc",
		"newline":   "abcdefgh\nINJECTED",
		"too long":  strings.Repeat("a", 65),
	} {
		t.Run(name, func(t *testing.T) {
			var seen string
			h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = observability.RequestIDFrom(r.Context())
			}))
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if incoming != "" {
				r.Header.Set(RequestIDHeader, incoming)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			assert.Len(t, seen, 32)
			assert.NotEqual(t, incoming, seen)
			assert.Equal(t, seen, w.Header().Get(RequestIDHeader))
		})
	}
}

func TestRequestIDReusesValidIncomingID(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = observability.RequestIDFrom(r.Context())
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set(RequestIDHeader, "proxy-id_123.abc")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	assert.Equal(t, "proxy-id_123.abc", seen)
	assert.Equal(t, "proxy-id_123.abc", w.Header().Get(RequestIDHeader))
}

func TestLocaleFromAcceptLanguage(t *testing.T) {
	var seen i18n.Locale
	h := Locale(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = i18n.FromContext(r.Context())
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept-Language", "en-GB,en;q=0.9")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	assert.Equal(t, i18n.EnUS, seen)
	assert.Equal(t, "Accept-Language", w.Header().Get("Vary"))
}

func TestSecurityHeaders(t *testing.T) {
	for _, hsts := range []bool{false, true} {
		w := httptest.NewRecorder()
		SecurityHeaders(hsts)(ok).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		h := w.Header()

		assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
		assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
		assert.Contains(t, h.Get("Content-Security-Policy"), "frame-ancestors 'none'")
		assert.Equal(t, "strict-origin-when-cross-origin", h.Get("Referrer-Policy"))
		assert.Equal(t, "same-origin", h.Get("Cross-Origin-Opener-Policy"))
		assert.Contains(t, h.Get("Permissions-Policy"), "camera=()")
		assert.Equal(t, "no-store", h.Get("Cache-Control"))
		assert.Empty(t, h.Get("Server"))
		if hsts {
			assert.Equal(t, "max-age=63072000; includeSubDomains; preload", h.Get("Strict-Transport-Security"))
		} else {
			assert.Empty(t, h.Get("Strict-Transport-Security"))
		}
	}
}

func TestBodyLimitRejectsDeclaredLength(t *testing.T) {
	called := false
	h := BodyLimit(10)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 11)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	assert.False(t, called)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Equal(t, problem.ContentType, w.Header().Get("Content-Type"))
}

func TestBodyLimitCapsUndeclaredLength(t *testing.T) {
	var readErr error
	h := BodyLimit(10)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 11)))
	r.ContentLength = -1 // chunked: size unknown up front
	h.ServeHTTP(httptest.NewRecorder(), r)

	var maxErr *http.MaxBytesError
	assert.ErrorAs(t, readErr, &maxErr)
}

func TestBodyLimitAllowsSmallBodies(t *testing.T) {
	var body []byte
	h := BodyLimit(10)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello")))
	assert.Equal(t, "hello", string(body))
}

func TestRecovererReturnsProblemAndLogsStack(t *testing.T) {
	var logs bytes.Buffer
	logger := observability.NewLogger(&logs, observability.LoggerOptions{Level: slog.LevelInfo})
	h := Recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	var p problem.Problem
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p))
	assert.Equal(t, "internal_error", p.Code)
	assert.NotContains(t, w.Body.String(), "boom", "panic value never reaches the client")
	assert.NotContains(t, w.Body.String(), "goroutine")

	assert.Contains(t, logs.String(), `"panic":"boom"`)
	assert.Contains(t, logs.String(), "goroutine")
}

func TestRecovererRepanicsOnAbortHandler(t *testing.T) {
	h := Recoverer(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}

func TestInstrumentRecordsRoutePatternAndLogs(t *testing.T) {
	var logs bytes.Buffer
	logger := observability.NewLogger(&logs, observability.LoggerOptions{Level: slog.LevelInfo})
	metrics := observability.NewMetrics("test", "local")

	r := chi.NewRouter()
	r.Use(RequestID, Instrument(logger, metrics))
	r.Get("/items/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("short"))
	})
	r.Get("/healthz", func(http.ResponseWriter, *http.Request) {})

	for _, path := range []string{"/items/1?token=secret", "/items/2", "/nope", "/healthz"} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	assert.Contains(t, body, `monthra_http_requests_total{method="GET",route="/items/{id}",status="418"} 2`)
	assert.Contains(t, body, `monthra_http_requests_total{method="GET",route="unmatched",status="404"} 1`)
	assert.Contains(t, body, `monthra_http_requests_total{method="GET",route="/healthz",status="200"} 1`)

	out := logs.String()
	assert.Contains(t, out, `"route":"/items/{id}"`)
	assert.Contains(t, out, `"path":"/items/1"`)
	assert.Contains(t, out, `"status":418`)
	assert.Contains(t, out, `"bytes":5`)
	assert.Contains(t, out, `"request_id":`)
	assert.NotContains(t, out, "secret", "query strings are never logged")
	assert.NotContains(t, out, `"path":"/healthz"`, "health probes log at debug only")
}

func TestInstrumentLogsServerErrorsAtErrorLevel(t *testing.T) {
	var logs bytes.Buffer
	logger := observability.NewLogger(&logs, observability.LoggerOptions{Level: slog.LevelInfo})
	h := Instrument(logger, observability.NewMetrics("test", "local"))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	assert.Contains(t, logs.String(), `"level":"ERROR"`)
}
