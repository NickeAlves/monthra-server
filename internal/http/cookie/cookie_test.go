package cookie

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNamesFollowThePolicy(t *testing.T) {
	secure := Policy{Secure: true}
	assert.Equal(t, "__Host-monthra_csrf", secure.Name(CSRF, RootPath))
	assert.Equal(t, "__Host-monthra_at", secure.Name(AccessToken, RootPath))
	assert.Equal(t, "__Secure-monthra_rt", secure.Name(RefreshToken, RefreshPath), "__Host- needs Path=/")

	local := Policy{}
	assert.Equal(t, "monthra_csrf", local.Name(CSRF, RootPath))
	assert.Equal(t, "monthra_rt", local.Name(RefreshToken, RefreshPath))
}

func TestNewBuildsStrictHostOnlyCookies(t *testing.T) {
	c := Policy{Secure: true}.New(RefreshToken, "v", RefreshPath, true, time.Hour)
	assert.Equal(t, "__Secure-monthra_rt", c.Name)
	assert.Equal(t, RefreshPath, c.Path)
	assert.Empty(t, c.Domain, "host-only")
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)
	assert.Equal(t, 3600, c.MaxAge)

	local := Policy{}.New(CSRF, "v", RootPath, false, 0)
	assert.False(t, local.Secure)
	assert.False(t, local.HttpOnly)
	assert.Zero(t, local.MaxAge, "session cookie")
}

func TestGetOnlyReadsTheServedName(t *testing.T) {
	p := Policy{Secure: true}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "monthra_at", Value: "planted-over-http"})
	_, ok := p.Get(r, AccessToken, RootPath)
	assert.False(t, ok, "an unprefixed look-alike is ignored")
	assert.False(t, p.HasAuth(r))

	r.AddCookie(&http.Cookie{Name: "__Host-monthra_at", Value: "real"})
	v, ok := p.Get(r, AccessToken, RootPath)
	assert.True(t, ok)
	assert.Equal(t, "real", v)
	assert.True(t, p.HasAuth(r))
}

func TestHasAuthSeesTheRefreshCookie(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	r.AddCookie(&http.Cookie{Name: "monthra_rt", Value: "x"})
	assert.True(t, Policy{}.HasAuth(r))

	empty := httptest.NewRequest(http.MethodPost, RefreshPath, nil)
	empty.AddCookie(&http.Cookie{Name: "monthra_rt", Value: ""})
	assert.False(t, Policy{}.HasAuth(empty), "an empty value is no credential")
}
