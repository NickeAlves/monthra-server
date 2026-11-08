// Package cookie names and builds the API cookies (§6.2, §6.3).
//
// Behind TLS (staging, production) names carry a browser-enforced prefix:
// __Host- for cookies on Path=/ (Secure, host-only, cannot be set by a
// subdomain) and __Secure- for cookies scoped to a narrower path, because
// __Host- requires Path=/. Locally there is no TLS, so cookies are plain.
package cookie

import (
	"net/http"
	"time"
)

// Base names. The served name depends on the Policy.
const (
	CSRF         = "monthra_csrf"
	AccessToken  = "monthra_at"
	RefreshToken = "monthra_rt"
)

// Paths of the cookies that are not site-wide.
const (
	RootPath    = "/"
	RefreshPath = "/api/v1/auth/refresh"
)

// Policy decides the cookie names and the Secure flag.
type Policy struct {
	// Secure is true when TLS terminates in front of the API (staging, production).
	Secure bool
}

// Name returns the served name of base for path.
func (p Policy) Name(base, path string) string {
	switch {
	case !p.Secure:
		return base
	case path == RootPath:
		return "__Host-" + base
	default:
		return "__Secure-" + base
	}
}

// New builds a SameSite=Strict, host-only cookie. A zero maxAge makes a
// session cookie.
func (p Policy) New(base, value, path string, httpOnly bool, maxAge time.Duration) *http.Cookie {
	// SameSite is always Strict. Secure is off only locally (no TLS), and
	// HttpOnly is off only for the CSRF cookie, which the page must read.
	return &http.Cookie{ //nolint:gosec // G124: the flags above are set on purpose by the caller and the policy
		Name:     p.Name(base, path),
		Value:    value,
		Path:     path,
		MaxAge:   int(maxAge / time.Second),
		Secure:   p.Secure,
		HttpOnly: httpOnly,
		SameSite: http.SameSiteStrictMode,
	}
}

// Get returns the value of the cookie base on path, if the request carries it.
// Only the served (prefixed) name counts, so an unprefixed look-alike planted
// over plain HTTP is ignored.
func (p Policy) Get(r *http.Request, base, path string) (string, bool) {
	c, err := r.Cookie(p.Name(base, path))
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// HasAuth reports whether the request carries an access or refresh cookie,
// that is, whether a browser is sending ambient credentials.
func (p Policy) HasAuth(r *http.Request) bool {
	if _, ok := p.Get(r, AccessToken, RootPath); ok {
		return true
	}
	_, ok := p.Get(r, RefreshToken, RefreshPath)
	return ok
}
