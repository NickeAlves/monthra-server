package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

// TokenVerifier verifies access tokens.
type TokenVerifier interface {
	Parse(token string) (auth.Claims, error)
}

// RevocationChecker tells whether an access token or its session was revoked.
type RevocationChecker interface {
	Revoked(ctx context.Context, jti, sid string) (bool, error)
}

// AuthOptions configures Authenticate.
type AuthOptions struct {
	Tokens      TokenVerifier
	Revocations RevocationChecker
	Cookies     cookie.Policy
	Logger      *slog.Logger
}

// Authenticate requires a valid access token, from the Authorization header
// (native clients) or the access cookie (browsers), never both. It checks the
// denylist (fail closed: Redis down means 503) and stores the caller as an
// auth.Principal in the context and as the user id of the access log.
func Authenticate(o AuthOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bearer, sentBearer := bearerToken(r)
			cookieToken, sentCookie := o.Cookies.Get(r, cookie.AccessToken, cookie.RootPath)
			if sentBearer && sentCookie {
				unauthorized(w, r, i18n.ErrAmbiguousCredentials)
				return
			}
			token := bearer
			if sentCookie {
				token = cookieToken
			}
			if token == "" {
				unauthorized(w, r, i18n.ErrUnauthorized)
				return
			}
			claims, err := o.Tokens.Parse(token)
			if err != nil {
				unauthorized(w, r, i18n.ErrUnauthorized)
				return
			}
			revoked, err := o.Revocations.Revoked(r.Context(), claims.ID, claims.SessionID)
			if err != nil {
				o.Logger.ErrorContext(r.Context(), "token denylist unavailable, rejecting the request", slog.String("error", err.Error()))
				problem.Write(w, r, http.StatusServiceUnavailable, i18n.ErrServiceUnavailable)
				return
			}
			if revoked {
				unauthorized(w, r, i18n.ErrUnauthorized)
				return
			}
			p := auth.PrincipalFromClaims(claims)
			if meta := observability.RequestMetaFrom(r.Context()); meta != nil {
				meta.UserID = p.UserID
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

// ByUser keys budgets by the authenticated user. It must run after Authenticate.
func ByUser(r *http.Request) string {
	p, _ := auth.PrincipalFrom(r.Context())
	return p.UserID
}

// bearerToken returns the token of an "Authorization: Bearer <token>" header
// and whether the header used the Bearer scheme at all.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(token), true
}

func unauthorized(w http.ResponseWriter, r *http.Request, key i18n.Key) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="monthra"`)
	problem.Write(w, r, http.StatusUnauthorized, key)
}
