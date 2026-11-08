package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// DefaultLocaleTTL bounds how long a cached locale lives without a write.
const DefaultLocaleTTL = time.Hour

// UserLocales caches the stored locale of each user
// (monthra:{env}:user:locale:{user_id}), so authenticated answers use it
// without reading MongoDB on every request.
//
// Readers fill a miss with SetIfAbsent and writers overwrite with Set: a
// reader holding a value read before a change can never replace the new one.
type UserLocales struct {
	client *redis.Client
	keys   Keys
	ttl    time.Duration
}

// NewUserLocales returns the cache on client. A ttl of zero means DefaultLocaleTTL.
func NewUserLocales(client *redis.Client, keys Keys, ttl time.Duration) UserLocales {
	if ttl <= 0 {
		ttl = DefaultLocaleTTL
	}
	return UserLocales{client: client, keys: keys, ttl: ttl}
}

func (c UserLocales) key(userID string) string { return c.keys.Key("user", "locale", userID) }

// Get returns the cached locale of userID, or false on a miss (or an
// unsupported cached value).
func (c UserLocales) Get(ctx context.Context, userID string) (i18n.Locale, bool, error) {
	v, err := c.client.Get(ctx, c.key(userID)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get cached locale: %w", err)
	}
	l, ok := i18n.Parse(v)
	return l, ok, nil
}

// SetIfAbsent caches l for userID unless a value is already there.
func (c UserLocales) SetIfAbsent(ctx context.Context, userID string, l i18n.Locale) error {
	if err := c.client.SetNX(ctx, c.key(userID), string(l), c.ttl).Err(); err != nil {
		return fmt.Errorf("cache locale: %w", err)
	}
	return nil
}

// Set caches l for userID, replacing any value.
func (c UserLocales) Set(ctx context.Context, userID string, l i18n.Locale) error {
	if err := c.client.Set(ctx, c.key(userID), string(l), c.ttl).Err(); err != nil {
		return fmt.Errorf("cache locale: %w", err)
	}
	return nil
}

// EraseUserData drops the cached locale of userID (account deletion).
func (c UserLocales) EraseUserData(ctx context.Context, userID string) error {
	if err := c.client.Del(ctx, c.key(userID)).Err(); err != nil {
		return fmt.Errorf("erase cached locale: %w", err)
	}
	return nil
}
