package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/csrf"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

// CSRFOptions configures the CSRF guard.
type CSRFOptions struct {
	Signer  csrf.Signer
	Cookies cookie.Policy
	Allowed OriginAllowlist
	Metrics *observability.Metrics
}

// CSRF guards mutating requests (POST, PUT, PATCH, DELETE) as §6.3 asks:
//
//  1. An Origin outside the allowlist is rejected. Without Origin, a request
//     carrying auth cookies is checked by its Referer instead, when present.
//  2. A request with both a Bearer token and an auth cookie is rejected, so
//     the two credential kinds are never mixed.
//  3. A browser request (it sends Origin or carries auth cookies) must echo
//     the CSRF cookie in X-CSRF-Token, and the token must be signed by us.
//  4. Anything else is a native client (Bearer, or the mobile app before
//     login): it carries no ambient credentials, so there is nothing to forge.
//
// Webhooks are mounted outside this guard.
func CSRF(o CSRFOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			hasAuthCookie := o.Cookies.HasAuth(r)
			origin := r.Header.Get("Origin")

			if origin != "" && !o.Allowed.Allows(origin) {
				o.reject(w, r, "origin", http.StatusForbidden, i18n.ErrOriginNotAllowed)
				return
			}
			if origin == "" && hasAuthCookie {
				if ref := r.Header.Get("Referer"); ref != "" && !o.refererAllowed(ref) {
					o.reject(w, r, "referer", http.StatusForbidden, i18n.ErrOriginNotAllowed)
					return
				}
			}
			if hasAuthCookie && hasBearer(r) {
				o.reject(w, r, "mixed_credentials", http.StatusForbidden, i18n.ErrAmbiguousCredentials)
				return
			}
			if (origin != "" || hasAuthCookie) && !o.tokenMatches(r) {
				o.reject(w, r, "token", http.StatusForbidden, i18n.ErrCSRFInvalid)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (o CSRFOptions) tokenMatches(r *http.Request) bool {
	header := r.Header.Get(CSRFHeader)
	cookieToken, ok := o.Cookies.Get(r, cookie.CSRF, cookie.RootPath)
	if header == "" || !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(cookieToken)) == 1 && o.Signer.Valid(header)
}

func (o CSRFOptions) refererAllowed(ref string) bool {
	origin, err := config.OriginOf(ref)
	return err == nil && o.Allowed[origin]
}

func (o CSRFOptions) reject(w http.ResponseWriter, r *http.Request, reason string, status int, key i18n.Key) {
	o.Metrics.CSRFRejected(reason)
	problem.Write(w, r, status, key)
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions || m == http.MethodTrace
}

func hasBearer(r *http.Request) bool {
	scheme, _, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	return ok && strings.EqualFold(scheme, "Bearer")
}
