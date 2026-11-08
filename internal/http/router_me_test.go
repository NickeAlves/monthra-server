package http

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/http/middleware"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

// goodTokens accepts the token "good" as user u1.
type goodTokens struct{}

func (goodTokens) Parse(token string) (auth.Claims, error) {
	if token != "good" {
		return auth.Claims{}, auth.ErrInvalidToken
	}
	return auth.Claims{SessionID: "s1", RegisteredClaims: jwt.RegisteredClaims{
		Subject: "u1", ID: "j1", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}}, nil
}

type profileStub struct{}

func (profileStub) Me(context.Context, string) (domain.User, error) {
	return domain.User{ID: "u1", Name: "Ana"}, nil
}
func (profileStub) UpdateName(context.Context, string, string) (domain.User, error) {
	return domain.User{ID: "u1"}, nil
}
func (profileStub) Preferences(context.Context, string) (domain.Preferences, error) {
	return domain.DefaultPreferences(i18n.EnUS), nil
}
func (profileStub) UpdatePreferences(context.Context, string, domain.PreferencesPatch) (domain.Preferences, error) {
	return domain.Preferences{}, &domain.ValidationError{Fields: []domain.FieldError{{Field: "theme", Code: domain.CodeInvalid}}}
}

type storedLocales map[string]i18n.Locale

func (s storedLocales) Locale(_ context.Context, userID string) (i18n.Locale, bool) {
	l, ok := s[userID]
	return l, ok
}

func newMeRouter(t *testing.T) http.Handler {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	cookies := cookie.Policy{Secure: true}
	perMinute := config.Rate{Limit: 1000, Period: time.Minute}
	return NewRouter(RouterOptions{
		Logger:         logger,
		Metrics:        observability.NewMetrics("test", "local"),
		Health:         handler.NewHealth(logger, time.Second, nil),
		MaxBodyBytes:   1 << 10,
		AllowedOrigins: []string{testOrigin},
		Cookies:        cookies,
		CSRFSigner:     testSigner(t),
		Limiter:        &memLimiter{},
		Keys:           cache.NewKeys(config.EnvLocal),
		APIRateByIP:    perMinute,

		Auth:            &handler.Auth{Service: okAuth{}, Cookies: cookies, Logger: logger},
		Authenticator:   middleware.AuthOptions{Tokens: goodTokens{}, Revocations: noRevocations{}, Cookies: cookies, Logger: logger},
		Me:              &handler.Me{Profile: profileStub{}, Cookies: cookies, Logger: logger},
		Locales:         storedLocales{"u1": i18n.EnUS},
		AuthRateByIP:    perMinute,
		RefreshRateByIP: perMinute,
		APIRateByUser:   perMinute,
	})
}

func TestMeRoutesNeedAuthentication(t *testing.T) {
	h := newMeRouter(t)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/me"},
		{http.MethodPatch, "/api/v1/me"},
		{http.MethodGet, "/api/v1/me/preferences"},
		{http.MethodPatch, "/api/v1/me/preferences"},
	} {
		w := do(h, rt.method, rt.path, `{}`, jsonHeaders)
		assert.Equal(t, http.StatusUnauthorized, w.Code, rt.method+" "+rt.path)
		assert.Equal(t, "pt-BR", w.Header().Get("Content-Language"), "before authentication, Accept-Language (none: pt-BR)")
	}

	bearer := map[string]string{"Authorization": "Bearer good"}
	w := do(h, http.MethodGet, "/api/v1/me", "", bearer)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"name":"Ana"`)
}

func TestMeAnswersInTheStoredLocale(t *testing.T) {
	h := newMeRouter(t)
	headers := map[string]string{"Authorization": "Bearer good", "Content-Type": "application/json", "Accept-Language": "es-ES"}

	w := do(h, http.MethodPatch, "/api/v1/me/preferences", `{"theme":"sepia"}`, headers)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "en-US", w.Header().Get("Content-Language"), "the stored locale wins over Accept-Language")
	assert.Contains(t, w.Body.String(), `"title":"Invalid data"`)

	w = do(h, http.MethodGet, "/api/v1/me/nothing-here", "", headers)
	assert.Equal(t, http.StatusNotFound, w.Code)

	headers["Authorization"] = "Bearer forged"
	w = do(h, http.MethodGet, "/api/v1/me", "", headers)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, "es-ES", w.Header().Get("Content-Language"), "an unknown caller keeps Accept-Language")
}
