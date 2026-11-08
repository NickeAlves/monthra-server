package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/csrf"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
)

func csrfHandler(t *testing.T) CSRFToken {
	t.Helper()
	s, err := csrf.NewSigner(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	return CSRFToken{Signer: s, Cookies: cookie.Policy{Secure: true}}
}

func csrfBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		CSRFToken string `json:"csrfToken"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.CSRFToken
}

func TestCSRFTokenIssuesCookieAndBody(t *testing.T) {
	h := csrfHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	token := csrfBody(t, w)
	assert.True(t, h.Signer.Valid(token))

	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	c := cookies[0]
	assert.Equal(t, "__Host-monthra_csrf", c.Name)
	assert.Equal(t, token, c.Value)
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.Secure)
	assert.False(t, c.HttpOnly, "the page must be able to read it")
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
}

func TestCSRFTokenReusesAValidCookie(t *testing.T) {
	h := csrfHandler(t)
	existing := h.Signer.Issue()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-monthra_csrf", Value: existing})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	assert.Equal(t, existing, csrfBody(t, w), "other tabs keep their token")
	assert.Empty(t, w.Result().Cookies())
}

func TestCSRFTokenReplacesAForgedCookie(t *testing.T) {
	h := csrfHandler(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/csrf", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-monthra_csrf", Value: "forged.value"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	token := csrfBody(t, w)
	assert.NotEqual(t, "forged.value", token)
	assert.True(t, h.Signer.Valid(token))
	assert.Len(t, w.Result().Cookies(), 1)
}
