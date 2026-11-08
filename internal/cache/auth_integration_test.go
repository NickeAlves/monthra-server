//go:build integration

package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/testutil"
)

func redisClient(t *testing.T) *redis.Client {
	t.Helper()
	_, addr := testutil.StartRedis(t)
	client, err := cache.Connect(context.Background(), config.Redis{Addr: addr})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRevocations(t *testing.T) {
	ctx := context.Background()
	client := redisClient(t)
	r := cache.NewRevocations(client, cache.NewKeys(config.EnvLocal))

	revoked, err := r.Revoked(ctx, "jti-1", "sid-1")
	require.NoError(t, err)
	assert.False(t, revoked)

	require.NoError(t, r.DenyToken(ctx, "jti-1", time.Minute))
	revoked, err = r.Revoked(ctx, "jti-1", "sid-1")
	require.NoError(t, err)
	assert.True(t, revoked, "denied token")
	revoked, err = r.Revoked(ctx, "jti-2", "sid-1")
	require.NoError(t, err)
	assert.False(t, revoked, "other tokens of the session are fine")

	require.NoError(t, r.RevokeSession(ctx, "sid-1", time.Minute))
	revoked, err = r.Revoked(ctx, "jti-2", "sid-1")
	require.NoError(t, err)
	assert.True(t, revoked, "revoked session")

	ttl, err := client.TTL(ctx, "monthra:local:session:revoked:sid-1").Result()
	require.NoError(t, err)
	assert.InDelta(t, time.Minute.Seconds(), ttl.Seconds(), 2)
	assert.Equal(t, int64(1), client.Exists(ctx, "monthra:local:jwt:deny:jti-1").Val())

	require.NoError(t, client.Close())
	_, err = r.Revoked(ctx, "jti-3", "sid-3")
	assert.Error(t, err, "callers must fail closed")
}

func TestLockout(t *testing.T) {
	ctx := context.Background()
	client := redisClient(t)
	policy := auth.DefaultLockoutPolicy
	policy.IPLockAfter = 7
	l := cache.NewLockout(client, cache.NewKeys(config.EnvLocal), policy)

	state, err := l.Check(ctx, "emailhash", "198.51.100.7")
	require.NoError(t, err)
	assert.Equal(t, auth.LoginState{}, state)

	for i := 1; i <= 4; i++ {
		state, err = l.RecordFailure(ctx, "emailhash", "198.51.100.7")
		require.NoError(t, err)
		assert.Equal(t, i, state.EmailFailures)
		assert.Zero(t, state.RetryAfter)
	}
	state, err = l.RecordFailure(ctx, "emailhash", "198.51.100.7")
	require.NoError(t, err)
	assert.Equal(t, time.Minute, state.RetryAfter, "the 5th failure locks the address")

	state, err = l.Check(ctx, "emailhash", "198.51.100.7")
	require.NoError(t, err)
	assert.InDelta(t, time.Minute.Seconds(), state.RetryAfter.Seconds(), 2)
	assert.Equal(t, 5, state.EmailFailures)
	assert.Equal(t, 5, state.IPFailures)

	state, err = l.Check(ctx, "otheremail", "198.51.100.7")
	require.NoError(t, err)
	assert.Zero(t, state.RetryAfter, "another address from the same IP is not locked yet")
	assert.Equal(t, 5, state.IPFailures)

	_, err = l.RecordFailure(ctx, "otheremail", "198.51.100.7")
	require.NoError(t, err)
	state, err = l.RecordFailure(ctx, "otheremail", "198.51.100.7")
	require.NoError(t, err)
	assert.Equal(t, time.Minute, state.RetryAfter, "the 7th failure locks the IP")
	state, err = l.Check(ctx, "thirdemail", "198.51.100.7")
	require.NoError(t, err)
	assert.Positive(t, state.RetryAfter, "every address from a locked IP waits")
	state, err = l.Check(ctx, "thirdemail", "203.0.113.9")
	require.NoError(t, err)
	assert.Zero(t, state.RetryAfter)

	require.NoError(t, l.Reset(ctx, "emailhash"))
	state, err = l.Check(ctx, "emailhash", "203.0.113.9")
	require.NoError(t, err)
	assert.Equal(t, auth.LoginState{}, state, "a success clears the address")
	state, err = l.Check(ctx, "emailhash", "198.51.100.7")
	require.NoError(t, err)
	assert.Equal(t, 7, state.IPFailures, "but not the IP")

	ttl, err := client.TTL(ctx, "monthra:local:auth:fail:ip:198.51.100.7").Result()
	require.NoError(t, err)
	assert.InDelta(t, time.Hour.Seconds(), ttl.Seconds(), 2)
}

func TestCountRegistration(t *testing.T) {
	ctx := context.Background()
	client := redisClient(t)
	l := cache.NewLockout(client, cache.NewKeys(config.EnvLocal), auth.DefaultLockoutPolicy)
	for i := 1; i <= 3; i++ {
		n, err := l.CountRegistration(ctx, "198.51.100.7")
		require.NoError(t, err)
		assert.Equal(t, i, n)
	}
	n, err := l.CountRegistration(ctx, "203.0.113.9")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	ttl, err := client.TTL(ctx, "monthra:local:auth:register:ip:198.51.100.7").Result()
	require.NoError(t, err)
	assert.InDelta(t, time.Hour.Seconds(), ttl.Seconds(), 2)
}
