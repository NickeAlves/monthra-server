package middleware

import (
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
)

// RateLimitOptions configures one RateLimit middleware.
type RateLimitOptions struct {
	Limiter ratelimit.Limiter
	Keys    cache.Keys
	Policy  ratelimit.Policy
	// Subject returns who the budget belongs to (client IP, user id, ...).
	Subject func(*http.Request) string
	Logger  *slog.Logger
	Metrics *observability.Metrics
}

// RateLimit consumes one request from the subject's budget and answers 429
// with Retry-After once it is spent. Every response carries the
// RateLimit-Limit, RateLimit-Remaining and RateLimit-Reset headers.
func RateLimit(o RateLimitOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := o.Keys.Key("rl", o.Policy.Name, o.Subject(r))
			res, err := o.Limiter.Allow(r.Context(), key, o.Policy.Rate)
			if err != nil {
				o.Metrics.RateLimitError(o.Policy.Name)
				if o.Policy.FailOpen {
					o.Logger.WarnContext(r.Context(), "rate limiter unavailable, letting the request through",
						slog.String("policy", o.Policy.Name), slog.String("error", err.Error()))
					next.ServeHTTP(w, r)
					return
				}
				o.Logger.ErrorContext(r.Context(), "rate limiter unavailable, rejecting the request",
					slog.String("policy", o.Policy.Name), slog.String("error", err.Error()))
				problem.Write(w, r, http.StatusServiceUnavailable, i18n.ErrServiceUnavailable)
				return
			}

			h := w.Header()
			h.Set("RateLimit-Limit", strconv.Itoa(res.Limit))
			h.Set("RateLimit-Remaining", strconv.Itoa(max(res.Remaining, 0)))
			h.Set("RateLimit-Reset", strconv.Itoa(ceilSeconds(res.ResetAfter)))
			if !res.Allowed {
				o.Metrics.RateLimitRejected(o.Policy.Name)
				h.Set("Retry-After", strconv.Itoa(max(ceilSeconds(res.RetryAfter), 1)))
				problem.Write(w, r, http.StatusTooManyRequests, i18n.ErrRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ByClientIP keys budgets by client address. IPv6 clients are grouped by
// their /64, which a single host usually controls entirely.
func ByClientIP(r *http.Request) string {
	a := ClientIPFrom(r.Context())
	switch {
	case !a.IsValid():
		return "unknown"
	case a.Is4():
		return a.String()
	default:
		return netip.PrefixFrom(a, 64).Masked().String()
	}
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int(math.Ceil(d.Seconds()))
}
