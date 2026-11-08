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

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

var discard = slog.New(slog.DiscardHandler)

func decodeProblem(t *testing.T, w *httptest.ResponseRecorder) problem.Problem {
	t.Helper()
	var p problem.Problem
	require.NoError(t, json.NewDecoder(w.Body).Decode(&p))
	return p
}

type stubTokens map[string]auth.Claims

func (s stubTokens) Parse(token string) (auth.Claims, error) {
	c, ok := s[token]
	if !ok {
		return auth.Claims{}, auth.ErrInvalidToken
	}
	return c, nil
}

type stubRevocations struct {
	revoked map[string]bool
	err     error
}

func (s stubRevocations) Revoked(_ context.Context, jti, sid string) (bool, error) {
	return s.revoked[jti] || s.revoked[sid], s.err
}

var validClaims = auth.Claims{SessionID: "sid-1", RegisteredClaims: jwt.RegisteredClaims{
	Subject: "user-1", ID: "jti-1", ExpiresAt: jwt.NewNumericDate(time.Date(2026, 11, 8, 12, 15, 0, 0, time.UTC)),
}}

func authRequest(t *testing.T, rev stubRevocations, mutate func(*http.Request)) (*httptest.ResponseRecorder, *auth.Principal, *observability.RequestMeta) {
	t.Helper()
	var seen *auth.Principal
	meta := &observability.RequestMeta{RequestID: "req-1"}
	h := Authenticate(AuthOptions{
		Tokens:      stubTokens{"good": validClaims},
		Revocations: rev,
		Cookies:     cookie.Policy{Secure: true},
		Logger:      discard,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFrom(r.Context())
		require.True(t, ok)
		seen = &p
		assert.Equal(t, "user-1", ByUser(r))
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r = r.WithContext(observability.WithRequestMeta(r.Context(), meta))
	mutate(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, seen, meta
}

func bearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func accessCookie(token string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "__Host-monthra_at", Value: token}) }
}

func TestAuthenticateAcceptsBearerOrCookie(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"bearer":           bearer("good"),
		"lowercase scheme": func(r *http.Request) { r.Header.Set("Authorization", "bearer good") },
		"cookie":           accessCookie("good"),
	} {
		w, p, meta := authRequest(t, stubRevocations{}, mutate)
		require.Equal(t, http.StatusOK, w.Code, name)
		assert.Equal(t, auth.Principal{UserID: "user-1", SessionID: "sid-1", TokenID: "jti-1", ExpiresAt: validClaims.ExpiresAt.UTC()}, *p, name)
		assert.Equal(t, "user-1", meta.UserID, "the access log gets the user id")
	}
}

func TestAuthenticateRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		rev    stubRevocations
		mutate func(*http.Request)
		code   string
	}{
		"nothing":           {mutate: func(*http.Request) {}, code: "unauthorized"},
		"empty bearer":      {mutate: bearer(""), code: "unauthorized"},
		"basic auth":        {mutate: func(r *http.Request) { r.SetBasicAuth("a", "b") }, code: "unauthorized"},
		"bad token":         {mutate: bearer("forged"), code: "unauthorized"},
		"unprefixed cookie": {mutate: func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "monthra_at", Value: "good"}) }, code: "unauthorized"},
		"bearer and cookie": {mutate: func(r *http.Request) { bearer("good")(r); accessCookie("good")(r) }, code: "ambiguous_credentials"},
		"denylisted token":  {rev: stubRevocations{revoked: map[string]bool{"jti-1": true}}, mutate: bearer("good"), code: "unauthorized"},
		"revoked session":   {rev: stubRevocations{revoked: map[string]bool{"sid-1": true}}, mutate: bearer("good"), code: "unauthorized"},
		"denylist is down":  {rev: stubRevocations{err: errors.New("redis down")}, mutate: bearer("good"), code: "service_unavailable"},
	} {
		w, p, _ := authRequest(t, tc.rev, tc.mutate)
		assert.Nil(t, p, name)
		assert.Equal(t, tc.code, decodeProblem(t, w).Code, name)
		if tc.code == "service_unavailable" {
			assert.Equal(t, http.StatusServiceUnavailable, w.Code, name)
			continue
		}
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
		assert.Equal(t, `Bearer realm="monthra"`, w.Header().Get("WWW-Authenticate"), name)
	}
}
