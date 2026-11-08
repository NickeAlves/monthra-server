//go:build integration

package cache_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

func TestUserLocales(t *testing.T) {
	ctx := context.Background()
	client := redisClient(t)
	c := cache.NewUserLocales(client, cache.NewKeys(config.EnvLocal), time.Minute)

	_, ok, err := c.Get(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, ok, "miss")

	require.NoError(t, c.SetIfAbsent(ctx, "u1", i18n.EsES))
	got, ok, err := c.Get(ctx, "u1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, i18n.EsES, got)

	// A reader filling a miss with an older value never beats a writer.
	require.NoError(t, c.Set(ctx, "u1", i18n.EnUS))
	require.NoError(t, c.SetIfAbsent(ctx, "u1", i18n.PtBR))
	got, _, err = c.Get(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, i18n.EnUS, got)

	ttl, err := client.TTL(ctx, "monthra:local:user:locale:u1").Result()
	require.NoError(t, err)
	assert.InDelta(t, time.Minute.Seconds(), ttl.Seconds(), 2)

	require.NoError(t, client.Set(ctx, "monthra:local:user:locale:u2", "fr-FR", 0).Err())
	_, ok, err = c.Get(ctx, "u2")
	require.NoError(t, err)
	assert.False(t, ok, "an unsupported value counts as a miss")

	require.NoError(t, c.EraseUserData(ctx, "u1"))
	require.NoError(t, c.EraseUserData(ctx, "u1"), "erasing twice is fine")
	_, ok, err = c.Get(ctx, "u1")
	require.NoError(t, err)
	assert.False(t, ok)

	assert.Equal(t, cache.DefaultLocaleTTL, func() time.Duration {
		d := cache.NewUserLocales(client, cache.NewKeys(config.EnvLocal), 0)
		require.NoError(t, d.Set(ctx, "u3", i18n.PtBR))
		ttl, err := client.TTL(ctx, "monthra:local:user:locale:u3").Result()
		require.NoError(t, err)
		return ttl.Round(time.Minute)
	}())
}
