//go:build integration

// Package testutil starts real MongoDB and Redis containers for integration tests.
package testutil

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Same versions as docker-compose.yml.
const (
	MongoImage   = "mongo:8.0.34"
	RedisImage   = "redis:8.8.4-alpine3.23"
	MailpitImage = "axllent/mailpit:v1.33.0"
)

// StartMongo runs MongoDB and returns its connection string.
func StartMongo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcmongo.Run(ctx, MongoImage)
	testcontainers.CleanupContainer(t, c)
	if err != nil && c != nil {
		logContainer(t, c)
	}
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
	if err != nil && c != nil {
		logContainer(t, c)
	}
	require.NoError(t, err)
	uri, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	return c, strings.TrimPrefix(uri, "redis://")
}

// Mailpit is a running Mailpit: SMTP on SMTPHost:SMTPPort, REST API on APIURL.
type Mailpit struct {
	SMTPHost string
	SMTPPort int
	APIURL   string
}

// StartMailpit runs Mailpit, which accepts any SMTP message without TLS or auth.
func StartMailpit(t *testing.T) Mailpit {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.Run(ctx, MailpitImage,
		testcontainers.WithExposedPorts("1025/tcp", "8025/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/readyz").WithPort("8025/tcp"),
			wait.ForListeningPort("1025/tcp").SkipInternalCheck(), // the image has no shell
		),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil && c != nil {
		logContainer(t, c)
	}
	require.NoError(t, err)
	host, err := c.Host(ctx)
	require.NoError(t, err)
	smtpPort, err := c.MappedPort(ctx, "1025/tcp")
	require.NoError(t, err)
	api, err := c.PortEndpoint(ctx, "8025/tcp", "http")
	require.NoError(t, err)
	return Mailpit{SMTPHost: host, SMTPPort: int(smtpPort.Num()), APIURL: api}
}

// logContainer writes the container output to the test log, so a container
// that dies on start (as seen on CI runners) can be diagnosed.
func logContainer(t *testing.T, c testcontainers.Container) {
	t.Helper()
	rc, err := c.Logs(context.Background())
	if err != nil {
		t.Logf("container logs unavailable: %v", err)
		return
	}
	defer func() { _ = rc.Close() }()
	out, _ := io.ReadAll(rc)
	t.Logf("container logs:\n%s", out)
}
