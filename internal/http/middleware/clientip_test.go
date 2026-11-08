package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

func clientIPOf(t *testing.T, trusted []string, remote string, xff ...string) string {
	t.Helper()
	prefixes := make([]netip.Prefix, len(trusted))
	for i, p := range trusted {
		prefixes[i] = netip.MustParsePrefix(p)
	}
	var seen netip.Addr
	h := ClientIP(prefixes)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = ClientIPFrom(r.Context())
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !seen.IsValid() {
		return ""
	}
	return seen.String()
}

func TestClientIP(t *testing.T) {
	proxies := []string{"10.0.0.0/8"}
	tests := map[string]struct {
		trusted []string
		remote  string
		xff     []string
		want    string
	}{
		"no proxy configured: the peer is the client": {
			remote: "203.0.113.9:5000", xff: []string{"1.2.3.4"}, want: "203.0.113.9",
		},
		"untrusted peer cannot spoof X-Forwarded-For": {
			trusted: proxies, remote: "203.0.113.9:5000", xff: []string{"1.2.3.4"}, want: "203.0.113.9",
		},
		"trusted proxy: rightmost untrusted hop wins": {
			trusted: proxies, remote: "10.0.0.2:443", xff: []string{"6.6.6.6, 198.51.100.7"}, want: "198.51.100.7",
		},
		"chain of trusted proxies is skipped": {
			trusted: proxies, remote: "10.0.0.2:443", xff: []string{"198.51.100.7, 10.1.1.1", "10.2.2.2"}, want: "198.51.100.7",
		},
		"malformed hop stops the walk at the last verified one": {
			trusted: proxies, remote: "10.0.0.2:443", xff: []string{"198.51.100.7, garbage"}, want: "10.0.0.2",
		},
		"hop with a port": {
			trusted: proxies, remote: "10.0.0.2:443", xff: []string{"198.51.100.7:1234"}, want: "198.51.100.7",
		},
		"trusted proxy without header": {
			trusted: proxies, remote: "10.0.0.2:443", want: "10.0.0.2",
		},
		"ipv6 peer": {
			remote: "[2001:db8::1]:443", want: "2001:db8::1",
		},
		"ipv4-mapped ipv6 is unmapped": {
			remote: "[::ffff:203.0.113.9]:443", want: "203.0.113.9",
		},
		"unparsable peer": {
			remote: "not-an-address", want: "",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, clientIPOf(t, tc.trusted, tc.remote, tc.xff...))
		})
	}
}

func TestByClientIPGroupsIPv6By64(t *testing.T) {
	subject := func(remote string) string {
		var got string
		h := ClientIP(nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = ByClientIP(r) }))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	assert.Equal(t, "203.0.113.9", subject("203.0.113.9:1"))
	assert.Equal(t, "2001:db8:1:2::/64", subject("[2001:db8:1:2:aaaa::1]:1"))
	assert.Equal(t, subject("[2001:db8:1:2::1]:1"), subject("[2001:db8:1:2:ffff::9]:1"))
	assert.Equal(t, "unknown", subject("garbage"))
}
