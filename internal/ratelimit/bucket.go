package ratelimit

import (
	"context"
	"net/netip"

	"github.com/NickeAlves/monthra-server/internal/cache"
)

// Bucket binds a Limiter to one policy, so callers only name the subject.
type Bucket struct {
	Limiter Limiter
	Keys    cache.Keys
	Policy  Policy
}

// Take consumes one request from subject's budget.
func (b Bucket) Take(ctx context.Context, subject string) (Result, error) {
	return b.Limiter.Allow(ctx, b.Keys.Key("rl", b.Policy.Name, subject), b.Policy.Rate)
}

// IPSubject is the budget subject of a client address. IPv6 clients are
// grouped by their /64, which a single host usually controls entirely.
func IPSubject(a netip.Addr) string {
	switch {
	case !a.IsValid():
		return "unknown"
	case a.Is4():
		return a.String()
	default:
		return netip.PrefixFrom(a, 64).Masked().String()
	}
}
