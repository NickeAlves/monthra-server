package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NickeAlves/monthra-server/internal/http/problem"
)

const appOrigin = "https://app.monthra.example"

func corsDo(method, origin string, headers map[string]string) (*httptest.ResponseRecorder, bool) {
	called := false
	h := CORS(NewOriginAllowlist([]string{appOrigin}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(method, "/api/v1/x", nil)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, called
}

func TestCORSAllowedOrigin(t *testing.T) {
	w, called := corsDo(http.MethodGet, appOrigin, nil)
	assert.True(t, called)
	assert.Equal(t, appOrigin, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, w.Header().Get("Access-Control-Expose-Headers"), "Retry-After")
	assert.Contains(t, w.Header().Values("Vary"), "Origin")
}

func TestCORSAllowedOriginWithDefaultPort(t *testing.T) {
	w, _ := corsDo(http.MethodGet, "https://APP.monthra.example:443", nil)
	assert.Equal(t, "https://APP.monthra.example:443", w.Header().Get("Access-Control-Allow-Origin"), "echoes what the browser sent")
}

func TestCORSOtherOriginGetsNoHeaders(t *testing.T) {
	w, called := corsDo(http.MethodGet, "https://evil.example", nil)
	assert.True(t, called, "the request still runs; the browser hides the answer")
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORSSameOriginOrNativeRequest(t *testing.T) {
	w, called := corsDo(http.MethodGet, "", nil)
	assert.True(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSPreflightAllowed(t *testing.T) {
	w, called := corsDo(http.MethodOptions, appOrigin, map[string]string{
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "content-type, x-csrf-token",
	})
	assert.False(t, called, "preflights never reach the routes")
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, appOrigin, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "PATCH")
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Headers"), CSRFHeader)
	assert.Equal(t, corsMaxAge, w.Header().Get("Access-Control-Max-Age"))
	assert.Contains(t, w.Header().Values("Vary"), "Access-Control-Request-Headers")
}

func TestCORSPreflightDenied(t *testing.T) {
	for _, origin := range []string{"https://evil.example", "null", "https://app.monthra.example.evil.example"} {
		w, called := corsDo(http.MethodOptions, origin, map[string]string{"Access-Control-Request-Method": "POST"})
		assert.False(t, called)
		assert.Equal(t, http.StatusForbidden, w.Code, origin)
		assert.Equal(t, problem.ContentType, w.Header().Get("Content-Type"))
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	}
}
