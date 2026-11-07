//go:build integration

// Package testutil starts real MongoDB and Redis containers for integration tests.
package testutil

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// Same versions as docker-compose.yml.
const (
	MongoImage = "mongo:8.0.34"
	RedisImage = "redis:8.8.4-alpine3.23"
)

// StartMongo runs MongoDB and returns its connection string.
func StartMongo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcmongo.Run(ctx, MongoImage)
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	uri, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	return uri
}

// StartRedis runs Redis and returns the container and its host:port address.
func StartRedis(t *testing.T) (*tcredis.RedisContainer, string) {
	t.Helper()
	ctx := context.Background()
	c, err := tcredis.Run(ctx, RedisImage)
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	uri, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	return c, strings.TrimPrefix(uri, "redis://")
}
