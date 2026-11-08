package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/usecase/profile"
)

type fakeProfile struct {
	user   domain.User
	err    error
	userID string
	name   string
	patch  domain.PreferencesPatch
}

func (f *fakeProfile) Me(_ context.Context, userID string) (domain.User, error) {
	f.userID = userID
	return f.user, f.err
}

func (f *fakeProfile) UpdateName(_ context.Context, userID, name string) (domain.User, error) {
	f.userID, f.name = userID, name
	u := f.user
	u.Name = name
	return u, f.err
}

func (f *fakeProfile) Preferences(_ context.Context, userID string) (domain.Preferences, error) {
	f.userID = userID
	return f.user.Preferences, f.err
}

func (f *fakeProfile) UpdatePreferences(_ context.Context, userID string, patch domain.PreferencesPatch) (domain.Preferences, error) {
	f.userID, f.patch = userID, patch
	return f.user.Preferences, f.err
}

func sampleUser() domain.User {
	u := domain.NewUser("Ana", "ana@example.com", "$argon2id$secret-hash", i18n.EsES, t0)
	u.ID = "u1"
	verified := t0
	u.EmailVerifiedAt = &verified
	return u
}

func newMeHandler(p *fakeProfile) *Me {
	return &Me{Profile: p, Cookies: cookie.Policy{Secure: true}, Logger: slog.New(slog.DiscardHandler)}
}

func asUser(r *http.Request) {
	*r = *r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "u1", SessionID: "s1", TokenID: "j1", ExpiresAt: t0.Add(15 * time.Minute)}))
}

func request(method, path, body string, mutate ...func(*http.Request)) *http.Request {
	r := post(path, body, mutate...)
	r.Method = method
	return r
}

func jsonOf(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&out))
	return out
}

func TestGetMe(t *testing.T) {
	svc := &fakeProfile{user: sampleUser()}
	h := newMeHandler(svc)

	w := serve(h.Get, request(http.MethodGet, "/api/v1/me", ""))
	assert.Equal(t, http.StatusUnauthorized, w.Code, "only behind Authenticate")

	w = serve(h.Get, request(http.MethodGet, "/api/v1/me", "", asUser))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID)
	raw := w.Body.String()
	assert.NotContains(t, raw, "argon2id", "never the hash")
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &body))
	assert.Equal(t, "Ana", body["name"])
	assert.Equal(t, "ana@example.com", body["email"])
	assert.Equal(t, true, body["emailVerified"])
	assert.Equal(t, map[string]any{"enabled": false, "recoveryCodesRemaining": float64(0)}, body["mfa"])
	assert.Equal(t, map[string]any{"locale": "es-ES", "theme": "system", "currency": "BRL"}, body["preferences"])
	assert.Equal(t, "2026-11-08T12:00:00Z", body["createdAt"])

	svc.user.MFA = domain.MFA{Secret: "e1:sealed", EnabledAt: &t0, RecoveryCodes: []string{"h1", "h2", "h3"}}
	w = serve(h.Get, request(http.MethodGet, "/api/v1/me", "", asUser))
	assert.NotContains(t, w.Body.String(), "sealed", "never the secret")
	assert.NotContains(t, w.Body.String(), "h1", "never the code hashes")
	w = serve(h.Get, request(http.MethodGet, "/api/v1/me", "", asUser))
	assert.Equal(t, map[string]any{"enabled": true, "recoveryCodesRemaining": float64(3)}, jsonOf(t, w)["mfa"])

	svc.err = profile.ErrAccountNotFound
	w = serve(h.Get, request(http.MethodGet, "/api/v1/me", "", asUser))
	assert.Equal(t, http.StatusUnauthorized, w.Code, "a deleted account is an invalid session")
	assert.Equal(t, "unauthorized", problemOf(t, w).Code)
}

func TestUpdateMe(t *testing.T) {
	svc := &fakeProfile{user: sampleUser()}
	h := newMeHandler(svc)

	w := serve(h.Update, request(http.MethodPatch, "/api/v1/me", `{"name":"Bia"}`, asUser))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Bia", svc.name)
	assert.Equal(t, "Bia", jsonOf(t, w)["name"])

	for name, body := range map[string]string{
		"unknown field":   `{"name":"Bia","email":"x@example.com"}`,
		"operator key":    `{"$set":{"email":"x@example.com"}}`,
		"missing name":    `{}`,
		"null name":       `{"name":null}`,
		"oversized name":  `{"name":"` + strings.Repeat("a", 401) + `"}`,
		"wrong type":      `{"name":42}`,
		"trailing object": `{"name":"Bia"}{}`,
	} {
		svc.name = ""
		w := serve(h.Update, request(http.MethodPatch, "/api/v1/me", body, asUser))
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, w.Code, name)
		assert.Empty(t, svc.name, name)
	}

	svc.err = &domain.ValidationError{Fields: []domain.FieldError{{Field: "name", Code: domain.CodeRequired}}}
	w = serve(h.Update, request(http.MethodPatch, "/api/v1/me", `{"name":"  "}`, asUser))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	p := problemOf(t, w)
	assert.Equal(t, "validation_failed", p.Code)
	require.Len(t, p.Errors, 1)
	assert.Equal(t, "name", p.Errors[0].Field)

	w = serve(h.Update, request(http.MethodPatch, "/api/v1/me", `{"name":"Bia"}`))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestPreferencesRoutes(t *testing.T) {
	svc := &fakeProfile{user: sampleUser()}
	h := newMeHandler(svc)

	w := serve(h.GetPreferences, request(http.MethodGet, "/api/v1/me/preferences", "", asUser))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, map[string]any{"locale": "es-ES", "theme": "system", "currency": "BRL"}, jsonOf(t, w))

	w = serve(h.UpdatePreferences, request(http.MethodPatch, "/api/v1/me/preferences", `{"theme":"dark","locale":null}`, asUser))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, svc.patch.Theme)
	assert.Equal(t, "dark", *svc.patch.Theme)
	assert.Nil(t, svc.patch.Locale, "null leaves the field as it is")
	assert.Nil(t, svc.patch.Currency)

	w = serve(h.UpdatePreferences, request(http.MethodPatch, "/api/v1/me/preferences", `{"timezone":"UTC"}`, asUser))
	assert.Equal(t, http.StatusBadRequest, w.Code, "only the known fields")

	svc.err = &domain.ValidationError{Fields: []domain.FieldError{{Field: "currency", Code: domain.CodeInvalid}}}
	w = serve(h.UpdatePreferences, request(http.MethodPatch, "/api/v1/me/preferences", `{"currency":"GBP"}`, asUser))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	svc.err = errors.New("mongo down")
	w = serve(h.GetPreferences, request(http.MethodGet, "/api/v1/me/preferences", "", asUser))
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	for _, hf := range []http.HandlerFunc{h.GetPreferences, h.UpdatePreferences} {
		w := serve(hf, request(http.MethodGet, "/api/v1/me/preferences", `{}`))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	}
}
