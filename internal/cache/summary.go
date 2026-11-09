package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultSummaryTTL bounds how long a cached dashboard summary lives
// (§5.4): what a missed invalidation can leave stale.
const DefaultSummaryTTL = 5 * time.Minute

// summaryVersionTTL outlives every summary entry. A version that expires
// (or is lost) only makes the next reads miss: versions are random, so a
// new one never names old entries.
const summaryVersionTTL = 24 * time.Hour

// Summaries caches the dashboard summaries of each user (cache-aside,
// §5.4) under a version of that user's data: monthra:{env}:summary:{user_id}:ver
// holds a random token, and each summary lives at
// monthra:{env}:summary:{user_id}:{token}:{slot} for the TTL. A write to the
// user's data bumps the token, which invalidates every summary at once
// (one write can change every month after it). A reader that read the
// version before a write may store what it computed under the old token,
// which nobody reads any more.
type Summaries struct {
	client *redis.Client
	keys   Keys
	ttl    time.Duration
}

// NewSummaries returns the cache on client. A ttl of zero means DefaultSummaryTTL.
func NewSummaries(client *redis.Client, keys Keys, ttl time.Duration) Summaries {
	if ttl <= 0 {
		ttl = DefaultSummaryTTL
	}
	return Summaries{client: client, keys: keys, ttl: ttl}
}

// newVersion returns a random version token.
func newVersion() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return hex.EncodeToString(b)
}

// Version returns the current version of userID's summaries, starting one
// when there is none, in one round trip (SET NX GET).
func (c Summaries) Version(ctx context.Context, userID string) (string, error) {
	fresh := newVersion()
	v, err := c.client.SetArgs(ctx, c.keys.SummaryVersion(userID), fresh,
		redis.SetArgs{Mode: "NX", Get: true, TTL: summaryVersionTTL}).Result()
	switch {
	case errors.Is(err, redis.Nil):
		return fresh, nil
	case err != nil:
		return "", fmt.Errorf("get summaries version: %w", err)
	}
	return v, nil
}

// Get returns the summary of userID at slot under version, or false on a
// miss.
func (c Summaries) Get(ctx context.Context, userID, version, slot string) ([]byte, bool, error) {
	v, err := c.client.Get(ctx, c.keys.Summary(userID, version, slot)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get cached summary: %w", err)
	}
	return v, true, nil
}

// Set caches the summary of userID at slot under version for the TTL.
func (c Summaries) Set(ctx context.Context, userID, version, slot string, value []byte) error {
	if err := c.client.Set(ctx, c.keys.Summary(userID, version, slot), value, c.ttl).Err(); err != nil {
		return fmt.Errorf("cache summary: %w", err)
	}
	return nil
}

// Bump starts a new version of userID's summaries, so none cached before
// is read again.
func (c Summaries) Bump(ctx context.Context, userID string) error {
	if err := c.client.Set(ctx, c.keys.SummaryVersion(userID), newVersion(), summaryVersionTTL).Err(); err != nil {
		return fmt.Errorf("bump summaries version: %w", err)
	}
	return nil
}

// EraseUserData drops the version of userID's summaries (account
// deletion): the summaries cached under it are never read again and
// expire within the TTL.
func (c Summaries) EraseUserData(ctx context.Context, userID string) error {
	if err := c.client.Del(ctx, c.keys.SummaryVersion(userID)).Err(); err != nil {
		return fmt.Errorf("erase summaries version: %w", err)
	}
	return nil
}
