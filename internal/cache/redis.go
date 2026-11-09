// Package cache holds the Redis client and the key naming used for caching,
// rate limiting and token state.
package cache

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"

	"github.com/NickeAlves/monthra-server/internal/config"
)

// Connect opens a traced client and pings it, so the service fails fast at
// boot when Redis is unreachable or the password is wrong.
func Connect(ctx context.Context, cfg config.Redis) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:                  cfg.Addr,
		Password:              cfg.Password,
		DB:                    cfg.DB,
		DialTimeout:           5 * time.Second,
		ReadTimeout:           3 * time.Second,
		WriteTimeout:          3 * time.Second,
		ContextTimeoutEnabled: true,
	})
	if err := redisotel.InstrumentTracing(client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("instrument redis: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

// SetLogger routes go-redis internal messages (pool and dial errors) through
// the JSON logger instead of plain text on stderr. It is process-wide.
func SetLogger(logger *slog.Logger) {
	redis.SetLogger(redisLogger{logger: logger.With(slog.String("component", "redis"))})
}

type redisLogger struct{ logger *slog.Logger }

func (l redisLogger) Printf(ctx context.Context, format string, v ...any) {
	l.logger.WarnContext(ctx, fmt.Sprintf(format, v...))
}

// Pinger adapts a client to the readiness probe.
type Pinger struct{ Client *redis.Client }

// Ping checks that Redis answers.
func (p Pinger) Ping(ctx context.Context) error { return p.Client.Ping(ctx).Err() }

// Keys builds Redis keys namespaced by environment: monthra:{env}:part:part...
// Every key goes through it, so environments sharing a Redis never collide.
type Keys struct{ prefix string }

// NewKeys returns the key builder for env.
func NewKeys(env config.Env) Keys { return Keys{prefix: "monthra:" + string(env)} }

// Key joins parts under the environment namespace.
func (k Keys) Key(parts ...string) string {
	return k.prefix + ":" + strings.Join(parts, ":")
}

// SummaryVersion is the key of the version of a user's cached dashboard
// summaries: monthra:{env}:summary:{user_id}:ver.
func (k Keys) SummaryVersion(userID string) string { return k.Key("summary", userID, "ver") }

// Summary is the cache-aside key of one dashboard summary of a user (§5.4)
// under a version of them: monthra:{env}:summary:{user_id}:{version}:{slot},
// slot naming the summary (its month, among others).
func (k Keys) Summary(userID, version, slot string) string {
	return k.Key("summary", userID, version, slot)
}
