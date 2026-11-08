//go:build integration

package cache_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
)

func TestMFAChallenges(t *testing.T) {
	ctx := context.Background()
	client := redisClient(t)
	m := cache.NewMFAChallenges(client, cache.NewKeys(config.EnvLocal))

	_, ok, err := m.Attempt(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, int64(0), client.Exists(ctx, "monthra:local:auth:mfa:missing").Val(), "an attempt never recreates a challenge")

	require.NoError(t, m.Create(ctx, "h1", auth.MFAChallenge{UserID: "u1", EmailKey: "e1"}, time.Minute))
	ttl := client.PTTL(ctx, "monthra:local:auth:mfa:h1").Val()
	assert.InDelta(t, time.Minute.Seconds(), ttl.Seconds(), 2)

	for want := 1; want <= 3; want++ {
		c, ok, err := m.Attempt(ctx, "h1")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, auth.MFAChallenge{UserID: "u1", EmailKey: "e1", Attempts: want}, c)
	}

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if ok, err := m.Consume(ctx, "h1"); err == nil && ok {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load(), "exactly one consumer wins")
	_, ok, err = m.Attempt(ctx, "h1")
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, m.Create(ctx, "h2", auth.MFAChallenge{UserID: "u2", EmailKey: "e2"}, 50*time.Millisecond))
	assert.Eventually(t, func() bool {
		_, ok, err := m.Attempt(ctx, "h2")
		return err == nil && !ok
	}, 2*time.Second, 20*time.Millisecond, "challenges expire")

	require.NoError(t, client.Close())
	_, _, err = m.Attempt(ctx, "h1")
	assert.Error(t, err, "callers must fail closed")
}

func TestRevokeSessions(t *testing.T) {
	ctx := context.Background()
	client := redisClient(t)
	r := cache.NewRevocations(client, cache.NewKeys(config.EnvLocal))

	require.NoError(t, r.RevokeSessions(ctx, nil, time.Minute))
	require.NoError(t, r.RevokeSessions(ctx, []string{"s1", "s2"}, time.Minute))
	for _, sid := range []string{"s1", "s2"} {
		revoked, err := r.Revoked(ctx, "any", sid)
		require.NoError(t, err)
		assert.True(t, revoked, sid)
	}
	revoked, err := r.Revoked(ctx, "any", "s3")
	require.NoError(t, err)
	assert.False(t, revoked)
}
