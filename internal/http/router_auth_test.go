package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/http/middleware"
	"github.com/NickeAlves/monthra-server/internal/observability"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
)

type okAuth struct{}

func (okAuth) Register(context.Context, authuc.RegisterInput, authuc.Client) error { return nil }
func (okAuth) VerifyEmail(context.Context, string, authuc.Client) error            { return nil }
func (okAuth) ResendVerification(context.Context, string, authuc.Client) error     { return nil }
func (okAuth) ForgotPassword(context.Context, string, authuc.Client) error         { return nil }
func (okAuth) ResetPassword(context.Context, authuc.ResetPasswordInput, authuc.Client) error {
	return nil
}
func (okAuth) Login(context.Context, authuc.LoginInput, authuc.Client) (authuc.LoginResult, error) {
	return authuc.LoginResult{}, nil
}
func (okAuth) VerifyMFA(context.Context, authuc.VerifyMFAInput, authuc.Client) (authuc.Tokens, error) {
	return authuc.Tokens{}, nil
}
func (okAuth) SetupMFA(context.Context, auth.Principal, string, authuc.Client) (authuc.MFASetup, error) {
	return authuc.MFASetup{}, nil
}
func (okAuth) EnableMFA(context.Context, auth.Principal, string, authuc.Client) ([]string, error) {
	return nil, nil
}
func (okAuth) DisableMFA(context.Context, auth.Principal, authuc.DisableMFAInput, authuc.Client) error {
	return nil
}
func (okAuth) Refresh(context.Context, string, authuc.Client) (authuc.Tokens, error) {
	return authuc.Tokens{}, nil
}
func (okAuth) Logout(context.Context, auth.Principal, authuc.Client) error { return nil }

type brokenLimiter struct{}

func (brokenLimiter) Allow(context.Context, string, config.Rate) (ratelimit.Result, error) {
	return ratelimit.Result{}, errors.New("redis down")
}

type noTokens struct{}

func (noTokens) Parse(string) (auth.Claims, error) { return auth.Claims{}, auth.ErrInvalidToken }

type noRevocations struct{}

func (noRevocations) Revoked(context.Context, string, string) (bool, error) { return false, nil }

func newAuthRouter(t *testing.T, limiter ratelimit.Limiter, authRate config.Rate) http.Handler {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	cookies := cookie.Policy{Secure: true}
	return NewRouter(RouterOptions{
		Logger:         logger,
		Metrics:        observability.NewMetrics("test", "local"),
		Health:         handler.NewHealth(logger, time.Second, nil),
		MaxBodyBytes:   1 << 10,
		AllowedOrigins: []string{testOrigin},
		Cookies:        cookies,
		CSRFSigner:     testSigner(t),
		Limiter:        limiter,
		Keys:           cache.NewKeys(config.EnvLocal),
		APIRateByIP:    config.Rate{Limit: 1000, Period: time.Minute},

		Auth:            &handler.Auth{Service: okAuth{}, Cookies: cookies, Logger: logger},
		Authenticator:   middleware.AuthOptions{Tokens: noTokens{}, Revocations: noRevocations{}, Cookies: cookies, Logger: logger},
		AuthRateByIP:    authRate,
		RefreshRateByIP: config.Rate{Limit: 1000, Period: time.Minute},
		APIRateByUser:   config.Rate{Limit: 1000, Period: time.Minute},
	})
}

var jsonHeaders = map[string]string{"Content-Type": "application/json"}

func TestAuthRoutesAreMounted(t *testing.T) {
	h := newAuthRouter(t, &memLimiter{}, config.Rate{Limit: 1000, Period: time.Minute})
	for path, tc := range map[string]struct {
		body string
		want int
	}{
		"/api/v1/auth/register":            {`{"name":"a","email":"b","password":"c"}`, http.StatusAccepted},
		"/api/v1/auth/verify-email":        {`{"token":"t"}`, http.StatusNoContent},
		"/api/v1/auth/verify-email/resend": {`{"email":"b"}`, http.StatusAccepted},
		"/api/v1/auth/login":               {`{"email":"b","password":"c"}`, http.StatusOK},
		"/api/v1/auth/forgot-password":     {`{"email":"b"}`, http.StatusAccepted},
		"/api/v1/auth/reset-password":      {`{"token":"t","password":"c"}`, http.StatusNoContent},
		"/api/v1/auth/mfa/verify":          {`{"mfaToken":"m","code":"c"}`, http.StatusOK},
		"/api/v1/auth/mfa/setup":           {`{"password":"c"}`, http.StatusUnauthorized},
		"/api/v1/auth/mfa/enable":          {`{"code":"c"}`, http.StatusUnauthorized},
		"/api/v1/auth/mfa/disable":         {`{"password":"c","code":"c"}`, http.StatusUnauthorized},
		"/api/v1/auth/refresh":             {`{"refreshToken":"r"}`, http.StatusOK},
		"/api/v1/auth/logout":              {``, http.StatusUnauthorized},
	} {
		assert.Equal(t, tc.want, do(h, http.MethodPost, path, tc.body, jsonHeaders).Code, path)
	}
	w := do(h, http.MethodPost, "/api/v1/auth/logout", "", map[string]string{"Authorization": "Bearer forged"})
	assert.Equal(t, http.StatusUnauthorized, w.Code, "logout needs a valid token")
}

func TestAuthBudgetByIP(t *testing.T) {
	h := newAuthRouter(t, &memLimiter{}, config.Rate{Limit: 2, Period: time.Minute})
	login := `{"email":"b","password":"c"}`
	assert.Equal(t, http.StatusOK, do(h, http.MethodPost, "/api/v1/auth/login", login, jsonHeaders).Code)
	assert.Equal(t, http.StatusAccepted, do(h, http.MethodPost, "/api/v1/auth/forgot-password", `{"email":"b"}`, jsonHeaders).Code)
	w := do(h, http.MethodPost, "/api/v1/auth/login", login, jsonHeaders)
	assert.Equal(t, http.StatusTooManyRequests, w.Code, "the credential endpoints share one budget")
	assert.Equal(t, http.StatusOK, do(h, http.MethodPost, "/api/v1/auth/refresh", `{"refreshToken":"r"}`, jsonHeaders).Code, "refresh has its own")
}

func TestAuthRoutesFailClosedWithoutRedis(t *testing.T) {
	h := newAuthRouter(t, brokenLimiter{}, config.Rate{Limit: 5, Period: time.Minute})
	w := do(h, http.MethodPost, "/api/v1/auth/login", `{"email":"b","password":"c"}`, jsonHeaders)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	w = do(h, http.MethodPost, "/api/v1/auth/refresh", `{"refreshToken":"r"}`, jsonHeaders)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	w = do(h, http.MethodGet, "/api/v1/auth/csrf", "", nil)
	assert.Equal(t, http.StatusOK, w.Code, "the general API budget fails open")
}
