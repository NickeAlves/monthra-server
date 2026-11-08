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
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

var issued = authuc.Tokens{
	AccessToken: "access-token", AccessExpiresAt: t0.Add(15 * time.Minute),
	RefreshToken: "refresh-token", RefreshExpiresAt: t0.Add(30 * 24 * time.Hour),
}

type fakeAuth struct {
	err       error
	register  authuc.RegisterInput
	reset     authuc.ResetPasswordInput
	login     authuc.LoginInput
	challenge *authuc.Challenge
	verify    authuc.VerifyMFAInput
	disable   authuc.DisableMFAInput
	password  string
	code      string
	token     string
	email     string
	principal auth.Principal
	client    authuc.Client
}

func (f *fakeAuth) Register(_ context.Context, in authuc.RegisterInput, c authuc.Client) error {
	f.register, f.client = in, c
	return f.err
}

func (f *fakeAuth) VerifyEmail(_ context.Context, token string, _ authuc.Client) error {
	f.token = token
	return f.err
}

func (f *fakeAuth) ResendVerification(_ context.Context, email string, _ authuc.Client) error {
	f.email = email
	return f.err
}

func (f *fakeAuth) ForgotPassword(_ context.Context, email string, _ authuc.Client) error {
	f.email = email
	return f.err
}

func (f *fakeAuth) ResetPassword(_ context.Context, in authuc.ResetPasswordInput, _ authuc.Client) error {
	f.reset = in
	return f.err
}

func (f *fakeAuth) Login(_ context.Context, in authuc.LoginInput, c authuc.Client) (authuc.LoginResult, error) {
	f.login, f.client = in, c
	if f.challenge != nil {
		return authuc.LoginResult{Challenge: f.challenge}, f.err
	}
	return authuc.LoginResult{Tokens: issued}, f.err
}

func (f *fakeAuth) VerifyMFA(_ context.Context, in authuc.VerifyMFAInput, _ authuc.Client) (authuc.Tokens, error) {
	f.verify = in
	return issued, f.err
}

func (f *fakeAuth) SetupMFA(_ context.Context, p auth.Principal, password string, _ authuc.Client) (authuc.MFASetup, error) {
	f.principal, f.password = p, password
	return authuc.MFASetup{Secret: "JBSWY3DPEHPK3PXP", URI: "otpauth://totp/x", ExpiresAt: t0.Add(15 * time.Minute)}, f.err
}

func (f *fakeAuth) EnableMFA(_ context.Context, p auth.Principal, code string, _ authuc.Client) ([]string, error) {
	f.principal, f.code = p, code
	return []string{"aaaa-bbbb-cccc-dddd"}, f.err
}

func (f *fakeAuth) DisableMFA(_ context.Context, p auth.Principal, in authuc.DisableMFAInput, _ authuc.Client) error {
	f.principal, f.disable = p, in
	return f.err
}

func (f *fakeAuth) Refresh(_ context.Context, token string, _ authuc.Client) (authuc.Tokens, error) {
	f.token = token
	return issued, f.err
}

func (f *fakeAuth) Logout(_ context.Context, p auth.Principal, _ authuc.Client) error {
	f.principal = p
	return f.err
}

func newAuthHandler(svc *fakeAuth) *Auth {
	return &Auth{Service: svc, Cookies: cookie.Policy{Secure: true}, Logger: slog.New(slog.DiscardHandler), Now: func() time.Time { return t0 }}
}

func post(path, body string, mutate ...func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "test-agent")
	r = r.WithContext(i18n.WithLocale(r.Context(), i18n.EnUS))
	for _, m := range mutate {
		m(r)
	}
	return r
}

func fromBrowser(r *http.Request) { r.Header.Set("Origin", "https://app.monthra.example") }

func serve(h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func problemOf(t *testing.T, w *httptest.ResponseRecorder) problem.Problem {
	t.Helper()
	assert.Equal(t, problem.ContentType, w.Header().Get("Content-Type"))
	var p problem.Problem
	require.NoError(t, json.NewDecoder(w.Body).Decode(&p))
	return p
}

func cookiesOf(w *httptest.ResponseRecorder) map[string]*http.Cookie {
	out := map[string]*http.Cookie{}
	for _, c := range w.Result().Cookies() {
		out[c.Name] = c
	}
	return out
}

func TestRegisterAnswers202(t *testing.T) {
	svc := &fakeAuth{}
	w := serve(newAuthHandler(svc).Register, post("/api/v1/auth/register", `{"name":"Ana","email":"ana@example.com","password":"pw","captchaToken":"c"}`))
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Empty(t, w.Body.String())
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, authuc.RegisterInput{Name: "Ana", Email: "ana@example.com", Password: "pw", CaptchaToken: "c"}, svc.register)
	assert.Equal(t, "test-agent", svc.client.UserAgent)
	assert.Equal(t, i18n.EnUS, svc.client.Locale)
}

func TestRegisterValidationErrors(t *testing.T) {
	svc := &fakeAuth{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "password", Code: "breached"}}}}
	w := serve(newAuthHandler(svc).Register, post("/api/v1/auth/register", `{"name":"Ana","email":"ana@example.com","password":"pw"}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	p := problemOf(t, w)
	assert.Equal(t, "validation_failed", p.Code)
	assert.Equal(t, []problem.FieldError{{Field: "password", Code: "breached"}}, p.Errors)

	w = serve(newAuthHandler(&fakeAuth{}).Register, post("/api/v1/auth/register", `{"email":"ana@example.com"}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, []problem.FieldError{{Field: "name", Code: "required"}, {Field: "password", Code: "required"}}, problemOf(t, w).Errors)

	w = serve(newAuthHandler(&fakeAuth{}).Register, post("/api/v1/auth/register", `{"name":"a","email":"b","password":"c","role":"admin"}`))
	assert.Equal(t, http.StatusBadRequest, w.Code, "unknown fields are rejected")
}

func TestVerifyAndResend(t *testing.T) {
	svc := &fakeAuth{}
	w := serve(newAuthHandler(svc).VerifyEmail, post("/api/v1/auth/verify-email", `{"token":"tok"}`))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "tok", svc.token)

	w = serve(newAuthHandler(svc).ResendVerification, post("/api/v1/auth/verify-email/resend", `{"email":"ana@example.com"}`))
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "ana@example.com", svc.email)

	svc.err = authuc.ErrTokenInvalid
	w = serve(newAuthHandler(svc).VerifyEmail, post("/api/v1/auth/verify-email", `{"token":"tok"}`))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "token_invalid", problemOf(t, w).Code)

	w = serve(newAuthHandler(svc).ResendVerification, post("/api/v1/auth/verify-email/resend", `nope`))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = serve(newAuthHandler(svc).VerifyEmail, post("/api/v1/auth/verify-email", `{}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestForgotAndResetPassword(t *testing.T) {
	svc := &fakeAuth{}
	w := serve(newAuthHandler(svc).ForgotPassword, post("/api/v1/auth/forgot-password", `{"email":"ana@example.com"}`))
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "ana@example.com", svc.email)

	w = serve(newAuthHandler(svc).ResetPassword, post("/api/v1/auth/reset-password", `{"token":"tok","password":"new password!"}`))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, authuc.ResetPasswordInput{Token: "tok", Password: "new password!"}, svc.reset)
	assert.Empty(t, cookiesOf(w), "native clients get no cookies")

	w = serve(newAuthHandler(svc).ResetPassword, post("/api/v1/auth/reset-password", `{"token":"tok","password":"new password!"}`, fromBrowser))
	assert.Equal(t, http.StatusNoContent, w.Code)
	cookies := cookiesOf(w)
	require.Contains(t, cookies, "__Host-monthra_at")
	require.Contains(t, cookies, "__Secure-monthra_rt")
	assert.Negative(t, cookies["__Host-monthra_at"].MaxAge, "the revoked session's cookies are cleared")

	svc.err = authuc.ErrTokenInvalid
	w = serve(newAuthHandler(svc).ResetPassword, post("/api/v1/auth/reset-password", `{"token":"tok","password":"new password!"}`, fromBrowser))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "token_invalid", problemOf(t, w).Code)
	assert.Empty(t, cookiesOf(w), "a refused reset changes nothing")

	w = serve(newAuthHandler(svc).ForgotPassword, post("/api/v1/auth/forgot-password", `{}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	w = serve(newAuthHandler(svc).ResetPassword, post("/api/v1/auth/reset-password", `{"token":"tok"}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestLoginFromABrowserSetsCookiesOnly(t *testing.T) {
	w := serve(newAuthHandler(&fakeAuth{}).Login, post("/api/v1/auth/login", `{"email":"ana@example.com","password":"pw"}`, fromBrowser))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.JSONEq(t, `{"expiresIn":900,"refreshExpiresIn":2592000}`, w.Body.String(), "no token in the body")

	c := cookiesOf(w)
	at := c["__Host-monthra_at"]
	require.NotNil(t, at)
	assert.Equal(t, "access-token", at.Value)
	assert.Equal(t, "/", at.Path)
	assert.Equal(t, 900, at.MaxAge)
	assert.True(t, at.HttpOnly && at.Secure)
	assert.Equal(t, http.SameSiteStrictMode, at.SameSite)
	assert.Empty(t, at.Domain, "host-only")

	rt := c["__Secure-monthra_rt"]
	require.NotNil(t, rt)
	assert.Equal(t, "refresh-token", rt.Value)
	assert.Equal(t, "/api/v1/auth/refresh", rt.Path, "sent only to the refresh route")
	assert.Equal(t, 30*24*3600, rt.MaxAge)
	assert.True(t, rt.HttpOnly && rt.Secure)
	assert.Equal(t, http.SameSiteStrictMode, rt.SameSite)
}

func TestLoginFromANativeClientReturnsTokens(t *testing.T) {
	svc := &fakeAuth{}
	w := serve(newAuthHandler(svc).Login, post("/api/v1/auth/login", `{"email":"ana@example.com","password":"pw","captchaToken":"c"}`))
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"tokenType":"Bearer","accessToken":"access-token","refreshToken":"refresh-token","expiresIn":900,"refreshExpiresIn":2592000}`, w.Body.String())
	assert.Empty(t, w.Result().Cookies())
	assert.Equal(t, authuc.LoginInput{Email: "ana@example.com", Password: "pw", CaptchaToken: "c"}, svc.login)
}

func TestRefreshReadsTheRightCredential(t *testing.T) {
	svc := &fakeAuth{}
	h := newAuthHandler(svc)

	r := post("/api/v1/auth/refresh", ``, fromBrowser)
	r.AddCookie(&http.Cookie{Name: "__Secure-monthra_rt", Value: "from-cookie"})
	w := serve(h.Refresh, r)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "from-cookie", svc.token)
	assert.NotNil(t, cookiesOf(w)["__Host-monthra_at"])

	r = post("/api/v1/auth/refresh", `{"refreshToken":"ignored"}`, fromBrowser)
	w = serve(h.Refresh, r)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, svc.token, "browsers never send the token in the body")

	w = serve(h.Refresh, post("/api/v1/auth/refresh", `{"refreshToken":"from-body"}`))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "from-body", svc.token)
	assert.Contains(t, w.Body.String(), `"accessToken":"access-token"`)

	w = serve(h.Refresh, post("/api/v1/auth/refresh", `{}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func TestRefreshFailureClearsBrowserCookies(t *testing.T) {
	svc := &fakeAuth{err: authuc.ErrUnauthorized}
	r := post("/api/v1/auth/refresh", ``, fromBrowser)
	r.AddCookie(&http.Cookie{Name: "__Secure-monthra_rt", Value: "stale"})
	w := serve(newAuthHandler(svc).Refresh, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	c := cookiesOf(w)
	require.NotNil(t, c["__Host-monthra_at"])
	assert.Negative(t, c["__Host-monthra_at"].MaxAge, "deleted")
	assert.Equal(t, "/api/v1/auth/refresh", c["__Secure-monthra_rt"].Path)
	assert.Negative(t, c["__Secure-monthra_rt"].MaxAge)

	svc.err = authuc.ErrRefreshConflict
	w = serve(newAuthHandler(svc).Refresh, r)
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Empty(t, w.Result().Cookies(), "a conflict keeps the fresh cookies another request just set")
}

func TestLogout(t *testing.T) {
	svc := &fakeAuth{}
	h := newAuthHandler(svc)
	w := serve(h.Logout, post("/api/v1/auth/logout", ``))
	assert.Equal(t, http.StatusUnauthorized, w.Code, "only behind Authenticate")

	p := auth.Principal{UserID: "u1", SessionID: "s1", TokenID: "j1"}
	r := post("/api/v1/auth/logout", ``)
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w = serve(h.Logout, r)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, p, svc.principal)
	assert.Negative(t, cookiesOf(w)["__Host-monthra_at"].MaxAge)

	svc.err = authuc.ErrUnavailable
	w = serve(h.Logout, r)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestErrorMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"invalid credentials": {authuc.ErrInvalidCredentials, http.StatusUnauthorized, "invalid_credentials"},
		"unauthorized":        {authuc.ErrUnauthorized, http.StatusUnauthorized, "unauthorized"},
		"not verified":        {authuc.ErrEmailNotVerified, http.StatusForbidden, "email_not_verified"},
		"captcha required":    {authuc.ErrCaptchaRequired, http.StatusForbidden, "captcha_required"},
		"captcha invalid":     {authuc.ErrCaptchaInvalid, http.StatusForbidden, "captcha_invalid"},
		"token invalid":       {authuc.ErrTokenInvalid, http.StatusBadRequest, "token_invalid"},
		"conflict":            {authuc.ErrRefreshConflict, http.StatusConflict, "refresh_conflict"},
		"unavailable":         {errors.Join(authuc.ErrUnavailable, errors.New("redis")), http.StatusServiceUnavailable, "service_unavailable"},
		"rate limited":        {&authuc.RateLimitedError{RetryAfter: 61500 * time.Millisecond}, http.StatusTooManyRequests, "rate_limited"},
		"anything else":       {errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	} {
		w := serve(newAuthHandler(&fakeAuth{err: tc.err}).Login, post("/api/v1/auth/login", `{"email":"a","password":"b"}`))
		assert.Equal(t, tc.status, w.Code, name)
		p := problemOf(t, w)
		assert.Equal(t, tc.code, p.Code, name)
		assert.NotEmpty(t, p.Title, name)
		if tc.status == http.StatusTooManyRequests {
			assert.Equal(t, "62", w.Header().Get("Retry-After"))
		}
	}
}

func TestLoginWithMFAAnswersAChallenge(t *testing.T) {
	svc := &fakeAuth{challenge: &authuc.Challenge{Token: "mfa-token", ExpiresAt: t0.Add(5 * time.Minute)}}
	for name, mutate := range map[string]func(*http.Request){"browser": fromBrowser, "native": func(*http.Request) {}} {
		w := serve(newAuthHandler(svc).Login, post("/api/v1/auth/login", `{"email":"ana@example.com","password":"pw"}`, mutate))
		assert.Equal(t, http.StatusOK, w.Code, name)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"), name)
		assert.Empty(t, cookiesOf(w), "%s: no cookies before the second factor", name)
		var body map[string]any
		require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
		assert.Equal(t, map[string]any{"mfaRequired": true, "mfaToken": "mfa-token", "expiresIn": float64(300)}, body, name)
	}
}

func TestVerifyMFADeliversTheSession(t *testing.T) {
	svc := &fakeAuth{}
	w := serve(newAuthHandler(svc).VerifyMFA, post("/api/v1/auth/mfa/verify", `{"mfaToken":"m","code":"123456"}`, fromBrowser))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, authuc.VerifyMFAInput{Token: "m", Code: "123456"}, svc.verify)
	assert.Contains(t, cookiesOf(w), "__Host-monthra_at", "browsers get the cookies now")

	w = serve(newAuthHandler(svc).VerifyMFA, post("/api/v1/auth/mfa/verify", `{"mfaToken":"m","code":"123456"}`))
	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "access-token", body["accessToken"])

	w = serve(newAuthHandler(svc).VerifyMFA, post("/api/v1/auth/mfa/verify", `{"mfaToken":"m"}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
}

func authenticated(r *http.Request) {
	*r = *r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "u1", SessionID: "s1"}))
}

func TestMFASettings(t *testing.T) {
	svc := &fakeAuth{}
	h := newAuthHandler(svc)

	w := serve(h.SetupMFA, post("/api/v1/auth/mfa/setup", `{"password":"pw"}`, authenticated))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "pw", svc.password)
	assert.Equal(t, "u1", svc.principal.UserID)
	var setup map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&setup))
	assert.Equal(t, map[string]any{"secret": "JBSWY3DPEHPK3PXP", "otpauthUri": "otpauth://totp/x", "expiresIn": float64(900)}, setup)

	w = serve(h.EnableMFA, post("/api/v1/auth/mfa/enable", `{"code":"123456"}`, authenticated))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "123456", svc.code)
	var enabled map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&enabled))
	assert.Equal(t, map[string]any{"recoveryCodes": []any{"aaaa-bbbb-cccc-dddd"}}, enabled)

	w = serve(h.DisableMFA, post("/api/v1/auth/mfa/disable", `{"password":"pw","code":"123456"}`, authenticated))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, authuc.DisableMFAInput{Password: "pw", Code: "123456"}, svc.disable)

	for path, handle := range map[string]http.HandlerFunc{"setup": h.SetupMFA, "enable": h.EnableMFA, "disable": h.DisableMFA} {
		w = serve(handle, post("/api/v1/auth/mfa/"+path, `{}`))
		assert.Equal(t, http.StatusUnauthorized, w.Code, "%s needs a principal", path)
		w = serve(handle, post("/api/v1/auth/mfa/"+path, `{}`, authenticated))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, path)
	}

	svc.err = authuc.ErrMFACodeInvalid
	for path, handle := range map[string]http.HandlerFunc{"setup": h.SetupMFA, "enable": h.EnableMFA} {
		w = serve(handle, post("/api/v1/auth/mfa/"+path, `{"password":"pw","code":"1"}`, authenticated))
		assert.Equal(t, http.StatusBadRequest, w.Code, "%s rejects the field it does not take", path)
	}
	w = serve(h.DisableMFA, post("/api/v1/auth/mfa/disable", `{"password":"pw","code":"1"}`, authenticated))
	assert.Equal(t, http.StatusForbidden, w.Code)
	w = serve(h.EnableMFA, post("/api/v1/auth/mfa/enable", `{"code":"1"}`, authenticated))
	assert.Equal(t, http.StatusForbidden, w.Code)
	w = serve(h.SetupMFA, post("/api/v1/auth/mfa/setup", `{"password":"pw"}`, authenticated))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestMFAErrorMapping(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"password incorrect": {authuc.ErrPasswordIncorrect, http.StatusForbidden, "password_incorrect"},
		"code invalid":       {authuc.ErrMFACodeInvalid, http.StatusForbidden, "mfa_code_invalid"},
		"challenge invalid":  {authuc.ErrMFAChallengeInvalid, http.StatusUnauthorized, "mfa_challenge_invalid"},
		"already enabled":    {authuc.ErrMFAAlreadyEnabled, http.StatusConflict, "mfa_already_enabled"},
		"not enabled":        {authuc.ErrMFANotEnabled, http.StatusConflict, "mfa_not_enabled"},
		"setup required":     {authuc.ErrMFASetupRequired, http.StatusConflict, "mfa_setup_required"},
	} {
		w := serve(newAuthHandler(&fakeAuth{err: tc.err}).DisableMFA, post("/api/v1/auth/mfa/disable", `{"password":"pw","code":"1"}`, authenticated))
		assert.Equal(t, tc.status, w.Code, name)
		assert.Equal(t, tc.code, problemOf(t, w).Code, name)
	}
}

func TestSecondsAndNow(t *testing.T) {
	assert.Equal(t, 0, seconds(-time.Minute))
	assert.Equal(t, 90, seconds(90*time.Second+500*time.Millisecond))
	h := &Auth{}
	assert.WithinDuration(t, time.Now(), h.now(), time.Second)
}
