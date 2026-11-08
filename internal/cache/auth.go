package cache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/NickeAlves/monthra-server/internal/auth"
)

// Revocations is the access token denylist (§6.2). An access token is
// rejected when its jti was denied (logout) or its session was revoked
// (logout, refresh token reuse). Entries live as long as an access token can,
// after which expiry alone rejects the token.
type Revocations struct {
	client *redis.Client
	keys   Keys
}

// NewRevocations returns the denylist on client.
func NewRevocations(client *redis.Client, keys Keys) Revocations {
	return Revocations{client: client, keys: keys}
}

func (r Revocations) tokenKey(jti string) string   { return r.keys.Key("jwt", "deny", jti) }
func (r Revocations) sessionKey(sid string) string { return r.keys.Key("session", "revoked", sid) }

// DenyToken rejects the access token jti for ttl.
func (r Revocations) DenyToken(ctx context.Context, jti string, ttl time.Duration) error {
	if err := r.client.Set(ctx, r.tokenKey(jti), "1", max(ttl, time.Second)).Err(); err != nil {
		return fmt.Errorf("deny token: %w", err)
	}
	return nil
}

// RevokeSession rejects every access token of session sid for ttl.
func (r Revocations) RevokeSession(ctx context.Context, sid string, ttl time.Duration) error {
	if err := r.client.Set(ctx, r.sessionKey(sid), "1", max(ttl, time.Second)).Err(); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeSessions rejects every access token of the sessions sids for ttl, in one round trip.
func (r Revocations) RevokeSessions(ctx context.Context, sids []string, ttl time.Duration) error {
	if len(sids) == 0 {
		return nil
	}
	pipe := r.client.Pipeline()
	for _, sid := range sids {
		pipe.Set(ctx, r.sessionKey(sid), "1", max(ttl, time.Second))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	return nil
}

// Revoked reports whether the token jti or its session sid was revoked, in
// one round trip. Callers must reject the request when it errors (fail closed).
func (r Revocations) Revoked(ctx context.Context, jti, sid string) (bool, error) {
	vals, err := r.client.MGet(ctx, r.tokenKey(jti), r.sessionKey(sid)).Result()
	if err != nil {
		return false, fmt.Errorf("check revocations: %w", err)
	}
	for _, v := range vals {
		if v != nil {
			return true, nil
		}
	}
	return false, nil
}

// Lockout counts failed logins by email and by IP and locks them following
// an auth.LockoutPolicy. Email subjects arrive hashed.
type Lockout struct {
	client *redis.Client
	keys   Keys
	policy auth.LockoutPolicy
}

// NewLockout returns the lockout on client.
func NewLockout(client *redis.Client, keys Keys, policy auth.LockoutPolicy) Lockout {
	return Lockout{client: client, keys: keys, policy: policy}
}

func (l Lockout) failKey(kind, subject string) string {
	return l.keys.Key("auth", "fail", kind, subject)
}
func (l Lockout) lockKey(kind, subject string) string {
	return l.keys.Key("auth", "lock", kind, subject)
}

// Check returns the current lock and failure counts.
func (l Lockout) Check(ctx context.Context, email, ip string) (auth.LoginState, error) {
	pipe := l.client.Pipeline()
	lockEmail := pipe.PTTL(ctx, l.lockKey("email", email))
	lockIP := pipe.PTTL(ctx, l.lockKey("ip", ip))
	failEmail := pipe.Get(ctx, l.failKey("email", email))
	failIP := pipe.Get(ctx, l.failKey("ip", ip))
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return auth.LoginState{}, fmt.Errorf("check lockout: %w", err)
	}
	return auth.LoginState{
		RetryAfter:    max(lockEmail.Val(), lockIP.Val(), 0),
		EmailFailures: count(failEmail),
		IPFailures:    count(failIP),
	}, nil
}

// RecordFailure counts a failed login and starts a lock once a threshold is reached.
func (l Lockout) RecordFailure(ctx context.Context, email, ip string) (auth.LoginState, error) {
	pipe := l.client.TxPipeline()
	nEmail := pipe.Incr(ctx, l.failKey("email", email))
	pipe.Expire(ctx, l.failKey("email", email), l.policy.Window)
	nIP := pipe.Incr(ctx, l.failKey("ip", ip))
	pipe.Expire(ctx, l.failKey("ip", ip), l.policy.Window)
	if _, err := pipe.Exec(ctx); err != nil {
		return auth.LoginState{}, fmt.Errorf("record login failure: %w", err)
	}
	state := auth.LoginState{EmailFailures: int(nEmail.Val()), IPFailures: int(nIP.Val())}
	locks := l.client.Pipeline()
	if d := l.policy.LockFor(state.EmailFailures, l.policy.EmailLockAfter); d > 0 {
		locks.Set(ctx, l.lockKey("email", email), "1", d)
		state.RetryAfter = d
	}
	if d := l.policy.LockFor(state.IPFailures, l.policy.IPLockAfter); d > 0 {
		locks.Set(ctx, l.lockKey("ip", ip), "1", d)
		state.RetryAfter = max(state.RetryAfter, d)
	}
	if locks.Len() > 0 {
		if _, err := locks.Exec(ctx); err != nil {
			return auth.LoginState{}, fmt.Errorf("start lock: %w", err)
		}
	}
	return state, nil
}

// Reset forgets the failures of an address after a successful login. The IP
// counter stays: one success must not unlock guessing at other accounts.
func (l Lockout) Reset(ctx context.Context, email string) error {
	if err := l.client.Del(ctx, l.failKey("email", email), l.lockKey("email", email)).Err(); err != nil {
		return fmt.Errorf("reset lockout: %w", err)
	}
	return nil
}

// CountRegistration counts a sign-up from ip in a fixed window and returns the count.
func (l Lockout) CountRegistration(ctx context.Context, ip string) (int, error) {
	key := l.keys.Key("auth", "register", "ip", ip)
	pipe := l.client.TxPipeline()
	n := pipe.Incr(ctx, key)
	pipe.ExpireNX(ctx, key, l.policy.Window)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("count registration: %w", err)
	}
	return int(n.Val()), nil
}

func count(cmd *redis.StringCmd) int {
	n, err := strconv.Atoi(cmd.Val())
	if err != nil {
		return 0
	}
	return n
}
