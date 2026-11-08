package middleware

import (
	"net/http"
	"strings"

	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// CSRFHeader carries the CSRF token on mutating browser requests.
const CSRFHeader = "X-CSRF-Token"

var (
	corsAllowMethods  = "GET, HEAD, POST, PUT, PATCH, DELETE"
	corsAllowHeaders  = strings.Join([]string{"Accept", "Accept-Language", "Authorization", "Content-Type", CSRFHeader, RequestIDHeader}, ", ")
	corsExposeHeaders = strings.Join([]string{"Content-Language", "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset", "Retry-After", RequestIDHeader}, ", ")
)

// corsMaxAge is how long browsers may cache a preflight answer, in seconds.
const corsMaxAge = "600"

// OriginAllowlist is the exact set of web origins allowed to call the API
// from a browser. There are no wildcards.
type OriginAllowlist map[config.Origin]bool

// NewOriginAllowlist builds the allowlist from already normalized origins.
func NewOriginAllowlist(origins []string) OriginAllowlist {
	l := make(OriginAllowlist, len(origins))
	for _, o := range origins {
		l[config.Origin(o)] = true
	}
	return l
}

// Allows reports whether raw (an Origin header value) is in the list.
func (l OriginAllowlist) Allows(raw string) bool {
	o, err := config.ParseOrigin(raw)
	return err == nil && l[o]
}

// CORS answers preflights and adds the CORS response headers for allowed
// origins only, with credentials (§6.5). Requests from other origins get no
// CORS headers, so the browser keeps their responses from the calling page;
// the CSRF guard rejects their mutating requests outright.
func CORS(allowed OriginAllowlist) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			preflight := r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != ""
			h := w.Header()
			h.Add("Vary", "Origin")
			if preflight {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
			}
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !allowed.Allows(origin) {
				if preflight {
					problem.Write(w, r, http.StatusForbidden, i18n.ErrOriginNotAllowed)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			if preflight {
				h.Set("Access-Control-Allow-Methods", corsAllowMethods)
				h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
				h.Set("Access-Control-Max-Age", corsMaxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
			next.ServeHTTP(w, r)
		})
	}
}
