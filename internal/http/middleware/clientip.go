package middleware

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type clientIPKey struct{}

// ClientIP stores the client address in the request context (§6.4).
//
// X-Forwarded-For is believed only when the TCP peer is one of trusted: the
// header is walked right to left, skipping trusted proxies, and the first
// untrusted hop is the client. Anything a client writes to the left of that
// hop is ignored, so it cannot spoof its address to dodge rate limits.
func ClientIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			client := peerAddr(r.RemoteAddr)
			if client.IsValid() && isTrusted(client) {
				hops := forwardedHops(r.Header.Values("X-Forwarded-For"))
				for i := len(hops) - 1; i >= 0 && isTrusted(client); i-- {
					hop, ok := parseHop(hops[i])
					if !ok {
						break // malformed entry: keep the last hop we could verify
					}
					client = hop
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, client)))
		})
	}
}

// ClientIPFrom returns the client address stored by ClientIP. It is the zero
// Addr when the middleware did not run or the peer address was unparsable.
func ClientIPFrom(ctx context.Context) netip.Addr {
	a, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return a
}

func peerAddr(remote string) netip.Addr {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap()
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func forwardedHops(values []string) []string {
	var hops []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				hops = append(hops, part)
			}
		}
	}
	return hops
}

func parseHop(s string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap(), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	return netip.Addr{}, false
}
