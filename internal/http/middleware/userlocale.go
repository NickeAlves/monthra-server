package middleware

import (
	"context"
	"net/http"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// LocaleResolver returns the stored locale of a user, or false to keep the
// request's own. It never fails the request.
type LocaleResolver interface {
	Locale(ctx context.Context, userID string) (i18n.Locale, bool)
}

// UserLocale makes authenticated answers use the caller's stored language
// instead of the Accept-Language one (§5.5). It must run after Authenticate;
// without a principal it changes nothing.
func UserLocale(resolver LocaleResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if p, ok := auth.PrincipalFrom(r.Context()); ok {
				if l, ok := resolver.Locale(r.Context(), p.UserID); ok {
					r = r.WithContext(i18n.WithLocale(r.Context(), l))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
