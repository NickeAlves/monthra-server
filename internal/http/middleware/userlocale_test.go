package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

type fixedLocales map[string]i18n.Locale

func (f fixedLocales) Locale(_ context.Context, userID string) (i18n.Locale, bool) {
	l, ok := f[userID]
	return l, ok
}

func TestUserLocale(t *testing.T) {
	var seen i18n.Locale
	h := UserLocale(fixedLocales{"u1": i18n.EnUS})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = i18n.FromContext(r.Context())
	}))
	request := func(userID string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		ctx := i18n.WithLocale(r.Context(), i18n.EsES)
		if userID != "" {
			ctx = auth.WithPrincipal(ctx, auth.Principal{UserID: userID})
		}
		return r.WithContext(ctx)
	}

	h.ServeHTTP(httptest.NewRecorder(), request("u1"))
	assert.Equal(t, i18n.EnUS, seen, "the stored locale wins over Accept-Language")

	h.ServeHTTP(httptest.NewRecorder(), request("u2"))
	assert.Equal(t, i18n.EsES, seen, "no stored locale keeps the request one")

	h.ServeHTTP(httptest.NewRecorder(), request(""))
	assert.Equal(t, i18n.EsES, seen, "anonymous requests keep the request one")
}
