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
)

func TestSummaries(t *testing.T) {
	ctx := context.Background()
	client := redisClient(t)
	c := cache.NewSummaries(client, cache.NewKeys(config.EnvLocal), time.Minute)

	// The first read starts a version; the next ones keep it.
	v1, err := c.Version(ctx, "u1")
	require.NoError(t, err)
	assert.Len(t, v1, 32)
	again, err := c.Version(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, v1, again)
	ttl, err := client.TTL(ctx, "monthra:local:summary:u1:ver").Result()
	require.NoError(t, err)
	assert.InDelta(t, (24 * time.Hour).Seconds(), ttl.Seconds(), 5)
	other, err := c.Version(ctx, "u2")
	require.NoError(t, err)
	assert.NotEqual(t, v1, other)

	_, ok, err := c.Get(ctx, "u1", v1, "v1:2026-11:2026-11-08")
	require.NoError(t, err)
	assert.False(t, ok, "miss")
	require.NoError(t, c.Set(ctx, "u1", v1, "v1:2026-11:2026-11-08", []byte(`{"m":"2026-11"}`)))
	got, ok, err := c.Get(ctx, "u1", v1, "v1:2026-11:2026-11-08")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.JSONEq(t, `{"m":"2026-11"}`, string(got))
	ttl, err = client.TTL(ctx, "monthra:local:summary:u1:"+v1+":v1:2026-11:2026-11-08").Result()
	require.NoError(t, err)
	assert.InDelta(t, time.Minute.Seconds(), ttl.Seconds(), 2)

	// A write bumps the version: what was cached before is never read.
	require.NoError(t, c.Bump(ctx, "u1"))
	v2, err := c.Version(ctx, "u1")
	require.NoError(t, err)
	assert.NotEqual(t, v1, v2)
	_, ok, err = c.Get(ctx, "u1", v2, "v1:2026-11:2026-11-08")
	require.NoError(t, err)
	assert.False(t, ok)
	keep, err := c.Version(ctx, "u2")
	require.NoError(t, err)
	assert.Equal(t, other, keep, "another user's version stays")

	// A lost version (erased, or expired) starts a new one: never the old.
	require.NoError(t, c.Set(ctx, "u1", v2, "v1:2026-11:2026-11-08", []byte(`{}`)))
	require.NoError(t, c.EraseUserData(ctx, "u1"))
	require.NoError(t, c.EraseUserData(ctx, "u1"), "erasing twice is fine")
	n, err := client.Exists(ctx, "monthra:local:summary:u1:ver").Result()
	require.NoError(t, err)
	assert.Zero(t, n)
	v3, err := c.Version(ctx, "u1")
	require.NoError(t, err)
	assert.NotEqual(t, v2, v3)
	_, ok, err = c.Get(ctx, "u1", v3, "v1:2026-11:2026-11-08")
	require.NoError(t, err)
	assert.False(t, ok)

	d := cache.NewSummaries(client, cache.NewKeys(config.EnvLocal), 0)
	require.NoError(t, d.Set(ctx, "u3", "v", "s", []byte(`{}`)))
	ttl, err = client.TTL(ctx, "monthra:local:summary:u3:v:s").Result()
	require.NoError(t, err)
	assert.Equal(t, cache.DefaultSummaryTTL, ttl.Round(time.Minute))
}
