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

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
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
	assert.Equal(t, map[string]any{"locale": "es-ES", "theme": "system", "currency": "BRL", "budgetAlerts": true}, body["preferences"])
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
	assert.Equal(t, map[string]any{"locale": "es-ES", "theme": "system", "currency": "BRL", "budgetAlerts": true}, jsonOf(t, w))

	w = serve(h.UpdatePreferences, request(http.MethodPatch, "/api/v1/me/preferences", `{"theme":"dark","locale":null}`, asUser))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, svc.patch.Theme)
	assert.Equal(t, "dark", *svc.patch.Theme)
	assert.Nil(t, svc.patch.Locale, "null leaves the field as it is")
	assert.Nil(t, svc.patch.Currency)
	assert.Nil(t, svc.patch.BudgetAlerts)

	w = serve(h.UpdatePreferences, request(http.MethodPatch, "/api/v1/me/preferences", `{"budgetAlerts":false}`, asUser))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, svc.patch.BudgetAlerts)
	assert.False(t, *svc.patch.BudgetAlerts)
	w = serve(h.UpdatePreferences, request(http.MethodPatch, "/api/v1/me/preferences", `{"budgetAlerts":"no"}`, asUser))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "a boolean")

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

type fakeAccount struct {
	err       error
	current   bool
	sessions  []authuc.SessionInfo
	principal auth.Principal
	change    authuc.ChangePasswordInput
	deleted   authuc.DeleteAccountInput
	revoked   string
	others    bool
}

func (f *fakeAccount) ChangePassword(_ context.Context, p auth.Principal, in authuc.ChangePasswordInput, _ authuc.Client) error {
	f.principal, f.change = p, in
	return f.err
}

func (f *fakeAccount) ListSessions(_ context.Context, p auth.Principal) ([]authuc.SessionInfo, error) {
	f.principal = p
	return f.sessions, f.err
}

func (f *fakeAccount) RevokeSession(_ context.Context, p auth.Principal, id string, _ authuc.Client) (bool, error) {
	f.principal, f.revoked = p, id
	return f.current, f.err
}

func (f *fakeAccount) RevokeOtherSessions(_ context.Context, p auth.Principal, _ authuc.Client) error {
	f.principal, f.others = p, true
	return f.err
}

func (f *fakeAccount) DeleteAccount(_ context.Context, p auth.Principal, in authuc.DeleteAccountInput, _ authuc.Client) error {
	f.principal, f.deleted = p, in
	return f.err
}

func newAccountHandler(a *fakeAccount) *Me {
	h := newMeHandler(&fakeProfile{user: sampleUser()})
	h.Account = a
	return h
}

// withRouteID sets the {id} URL parameter as chi does.
func withRouteID(id string) func(*http.Request) {
	return func(r *http.Request) {
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", id)
		*r = *r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rc))
	}
}

func TestChangePasswordRoute(t *testing.T) {
	a := &fakeAccount{}
	h := newAccountHandler(a)
	w := serve(h.ChangePassword, request(http.MethodPost, "/api/v1/me/password", `{"currentPassword":"old one","newPassword":"new one"}`, asUser))
	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, authuc.ChangePasswordInput{CurrentPassword: "old one", NewPassword: "new one"}, a.change)
	assert.Equal(t, "s1", a.principal.SessionID)

	w = serve(h.ChangePassword, request(http.MethodPost, "/api/v1/me/password", `{"newPassword":"new one"}`, asUser))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "the current password is required")

	a.err = authuc.ErrPasswordIncorrect
	w = serve(h.ChangePassword, request(http.MethodPost, "/api/v1/me/password", `{"currentPassword":"x","newPassword":"y"}`, asUser))
	assert.Equal(t, http.StatusForbidden, w.Code, "403, not 401: the session is fine")
	assert.Equal(t, "password_incorrect", problemOf(t, w).Code)
}

func TestSessionRoutes(t *testing.T) {
	a := &fakeAccount{sessions: []authuc.SessionInfo{
		{Session: domain.Session{ID: "s1", UserAgent: "ua", IP: "198.51.100.7", CreatedAt: t0, LastUsedAt: t0, ExpiresAt: t0.Add(time.Hour)}, Current: true},
		{Session: domain.Session{ID: "s2", UserID: "u1", CreatedAt: t0, LastUsedAt: t0, ExpiresAt: t0.Add(time.Hour)}},
	}}
	h := newAccountHandler(a)

	w := serve(h.ListSessions, request(http.MethodGet, "/api/v1/me/sessions", "", asUser))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var body struct {
		Sessions []map[string]any `json:"sessions"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.Len(t, body.Sessions, 2)
	assert.Equal(t, map[string]any{
		"id": "s1", "createdAt": "2026-11-08T12:00:00Z", "lastUsedAt": "2026-11-08T12:00:00Z", "expiresAt": "2026-11-08T13:00:00Z",
		"userAgent": "ua", "ip": "198.51.100.7", "current": true,
	}, body.Sessions[0])
	assert.NotContains(t, body.Sessions[1], "userId", "no internal fields")

	a.sessions = nil
	w = serve(h.ListSessions, request(http.MethodGet, "/api/v1/me/sessions", "", asUser))
	assert.JSONEq(t, `{"sessions":[]}`, w.Body.String(), "an empty list, never null")

	w = serve(h.RevokeSession, request(http.MethodDelete, "/api/v1/me/sessions/s2", "", asUser, withRouteID("s2")))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "s2", a.revoked)
	assert.Empty(t, w.Result().Cookies())

	a.current = true
	w = serve(h.RevokeSession, request(http.MethodDelete, "/api/v1/me/sessions/s1", "", asUser, withRouteID("s1"), fromBrowser))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Negative(t, cookiesOf(w)["__Host-monthra_at"].MaxAge, "signing out the current browser session clears its cookies")

	a.err = authuc.ErrSessionNotFound
	w = serve(h.RevokeSession, request(http.MethodDelete, "/api/v1/me/sessions/x", "", asUser, withRouteID("x")))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", problemOf(t, w).Code)

	a.err = nil
	w = serve(h.RevokeOtherSessions, request(http.MethodDelete, "/api/v1/me/sessions", "", asUser))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.True(t, a.others)

	a.err = authuc.ErrUnavailable
	for _, hf := range []http.HandlerFunc{h.ListSessions, h.RevokeOtherSessions} {
		w := serve(hf, request(http.MethodGet, "/api/v1/me/sessions", "", asUser))
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	}
}

func TestDeleteMeRoute(t *testing.T) {
	a := &fakeAccount{}
	h := newAccountHandler(a)

	w := serve(h.Delete, request(http.MethodDelete, "/api/v1/me", `{"password":"secret","code":"123456"}`, asUser, fromBrowser))
	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, authuc.DeleteAccountInput{Password: "secret", Code: "123456"}, a.deleted)
	assert.Negative(t, cookiesOf(w)["__Host-monthra_at"].MaxAge, "cookies are cleared")
	assert.Negative(t, cookiesOf(w)["__Secure-monthra_rt"].MaxAge)

	a.deleted = authuc.DeleteAccountInput{}
	w = serve(h.Delete, request(http.MethodDelete, "/api/v1/me", ``, asUser))
	assert.Equal(t, http.StatusBadRequest, w.Code, "the password is asked in the body")
	assert.Empty(t, a.deleted.Password)

	a.err = authuc.ErrMFACodeInvalid
	w = serve(h.Delete, request(http.MethodDelete, "/api/v1/me", `{"password":"secret"}`, asUser))
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, "mfa_code_invalid", problemOf(t, w).Code)
	assert.Empty(t, w.Result().Cookies(), "a refused deletion keeps the session")

	for _, hf := range []http.HandlerFunc{h.ChangePassword, h.ListSessions, h.RevokeSession, h.RevokeOtherSessions, h.Delete} {
		w := serve(hf, request(http.MethodPost, "/api/v1/me", `{}`))
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	}
}
