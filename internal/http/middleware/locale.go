package middleware

import (
	"net/http"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Locale picks the response language from Accept-Language. On authenticated
// routes, UserLocale then replaces it with the caller's stored preference.
func Locale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l := i18n.FromAcceptLanguage(r.Header.Get("Accept-Language"))
		w.Header().Add("Vary", "Accept-Language")
		next.ServeHTTP(w, r.WithContext(i18n.WithLocale(r.Context(), l)))
	})
}
