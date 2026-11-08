// Package ratelimit enforces request budgets in Redis with GCRA (a token
// bucket without background refills), so every API instance shares them (§6.4).
package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/redis/go-redis/v9"

	"github.com/NickeAlves/monthra-server/internal/config"
)

// Policy is a named budget. Name ends up in Redis keys and metric labels, so
// it must be a short constant, never user input.
type Policy struct {
	Name string
	Rate config.Rate
	// FailOpen lets requests through when Redis is unreachable. Policies that
	// guard credentials (login, register, password reset) must fail closed.
	FailOpen bool
}

// Result is the outcome of one check.
type Result struct {
	Allowed   bool
	Limit     int
	Remaining int
	// RetryAfter is how long until the next request is allowed (zero when allowed).
	RetryAfter time.Duration
	// ResetAfter is how long until the budget is full again.
	ResetAfter time.Duration
}

// Limiter checks and consumes budgets.
type Limiter interface {
	Allow(ctx context.Context, key string, rate config.Rate) (Result, error)
}

// Redis is the Limiter backed by Redis. redis_rate stores each budget under
// "rate:" + key; callers pass keys already namespaced by environment
// (cache.Keys), so the stored key is rate:monthra:{env}:rl:...
type Redis struct{ limiter *redis_rate.Limiter }

// NewRedis returns a limiter on client.
func NewRedis(client *redis.Client) *Redis {
	return &Redis{limiter: redis_rate.NewLimiter(client)}
}

// Allow consumes one request from the budget of key.
func (r *Redis) Allow(ctx context.Context, key string, rate config.Rate) (Result, error) {
	res, err := r.limiter.Allow(ctx, key, redis_rate.Limit{Rate: rate.Limit, Burst: rate.Limit, Period: rate.Period})
	if err != nil {
		return Result{}, fmt.Errorf("rate limit %s: %w", key, err)
	}
	out := Result{
		Allowed:    res.Allowed > 0,
		Limit:      rate.Limit,
		Remaining:  res.Remaining,
		ResetAfter: max(res.ResetAfter, 0),
	}
	if !out.Allowed {
		out.RetryAfter = max(res.RetryAfter, 0)
	}
	return out, nil
}
