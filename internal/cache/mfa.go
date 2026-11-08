package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/NickeAlves/monthra-server/internal/auth"
)

// MFAChallenges holds the logins waiting for a second factor, by the hash of
// their mfaToken. Every call fails closed: an error means the caller rejects
// the request.
type MFAChallenges struct {
	client *redis.Client
	keys   Keys
}

// NewMFAChallenges returns the challenge store on client.
func NewMFAChallenges(client *redis.Client, keys Keys) MFAChallenges {
	return MFAChallenges{client: client, keys: keys}
}

func (m MFAChallenges) key(hash string) string { return m.keys.Key("auth", "mfa", hash) }

// Create stores a challenge for ttl.
func (m MFAChallenges) Create(ctx context.Context, hash string, c auth.MFAChallenge, ttl time.Duration) error {
	pipe := m.client.TxPipeline()
	pipe.HSet(ctx, m.key(hash), "user_id", c.UserID, "email_key", c.EmailKey, "attempts", 0)
	pipe.PExpire(ctx, m.key(hash), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("create mfa challenge: %w", err)
	}
	return nil
}

// attemptScript counts one attempt and returns it with the challenge, or
// nil when the challenge is gone. A plain HINCRBY would recreate an expired
// challenge (without a TTL).
var attemptScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then
  return false
end
local n = redis.call('HINCRBY', KEYS[1], 'attempts', 1)
local v = redis.call('HMGET', KEYS[1], 'user_id', 'email_key')
return {n, v[1], v[2]}
`)

// Attempt counts one more code tried on the challenge and returns it. It
// reports false when there is no such challenge (unknown, expired or used).
func (m MFAChallenges) Attempt(ctx context.Context, hash string) (auth.MFAChallenge, bool, error) {
	res, err := attemptScript.Run(ctx, m.client, []string{m.key(hash)}).Slice()
	if errors.Is(err, redis.Nil) {
		return auth.MFAChallenge{}, false, nil
	}
	if err != nil {
		return auth.MFAChallenge{}, false, fmt.Errorf("attempt mfa challenge: %w", err)
	}
	n, _ := res[0].(int64)
	userID, _ := res[1].(string)
	emailKey, _ := res[2].(string)
	if userID == "" {
		return auth.MFAChallenge{}, false, nil
	}
	return auth.MFAChallenge{UserID: userID, EmailKey: emailKey, Attempts: int(n)}, true, nil
}

// Consume deletes the challenge. It reports whether this call deleted it, so
// of two concurrent successful verifications only one opens a session.
func (m MFAChallenges) Consume(ctx context.Context, hash string) (bool, error) {
	n, err := m.client.Del(ctx, m.key(hash)).Result()
	if err != nil {
		return false, fmt.Errorf("consume mfa challenge: %w", err)
	}
	return n == 1, nil
}
