//go:build integration

package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
	"github.com/NickeAlves/monthra-server/internal/testutil"
)

func TestRedisLimiterSpendsAndRefusesTheBudget(t *testing.T) {
	ctx := context.Background()
	_, addr := testutil.StartRedis(t)
	client, err := cache.Connect(ctx, config.Redis{Addr: addr})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	limiter := ratelimit.NewRedis(client)
	key := cache.NewKeys(config.EnvLocal).Key("rl", "test", "198.51.100.7")
	rate := config.Rate{Limit: 3, Period: time.Minute}

	for i := range 3 {
		res, err := limiter.Allow(ctx, key, rate)
		require.NoError(t, err)
		assert.True(t, res.Allowed, "request %d", i+1)
		assert.Equal(t, 3, res.Limit)
		assert.Equal(t, 2-i, res.Remaining)
		assert.Zero(t, res.RetryAfter)
	}

	res, err := limiter.Allow(ctx, key, rate)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
	assert.Equal(t, 0, res.Remaining)
	assert.Greater(t, res.RetryAfter, time.Duration(0))
	assert.LessOrEqual(t, res.RetryAfter, 20*time.Second, "GCRA frees one slot every period/limit")

	other, err := limiter.Allow(ctx, key+"-other", rate)
	require.NoError(t, err)
	assert.True(t, other.Allowed, "budgets are per key")

	exists, err := client.Exists(ctx, "rate:"+key).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists, "stored under rate:monthra:{env}:rl:...")
}

func TestRedisLimiterReportsOutage(t *testing.T) {
	ctx := context.Background()
	c, addr := testutil.StartRedis(t)
	client, err := cache.Connect(ctx, config.Redis{Addr: addr})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	stop := 5 * time.Second
	require.NoError(t, c.Stop(ctx, &stop))

	_, err = ratelimit.NewRedis(client).Allow(ctx, "k", config.Rate{Limit: 1, Period: time.Second})
	assert.Error(t, err)
}
