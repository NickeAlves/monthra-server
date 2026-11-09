package cache

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/NickeAlves/monthra-server/internal/config"
)

func TestKeysAreNamespacedByEnv(t *testing.T) {
	assert.Equal(t, "monthra:local:rl:login:ip", NewKeys(config.EnvLocal).Key("rl", "login", "ip"))
	assert.Equal(t, "monthra:production:summary:u1:ver", NewKeys(config.EnvProduction).SummaryVersion("u1"))
	assert.Equal(t, "monthra:production:summary:u1:abc:v1:2026-11", NewKeys(config.EnvProduction).Summary("u1", "abc", "v1:2026-11"))
	assert.NotEqual(t,
		NewKeys(config.EnvStaging).Summary("u1", "abc", "v1:2026-11"),
		NewKeys(config.EnvProduction).Summary("u1", "abc", "v1:2026-11"),
	)
}

func TestRedisLoggerWritesThroughSlog(t *testing.T) {
	var buf bytes.Buffer
	l := redisLogger{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	l.Printf(context.Background(), "dial %s failed", "127.0.0.1:1")

	assert.Contains(t, buf.String(), `"level":"WARN"`)
	assert.Contains(t, buf.String(), `"msg":"dial 127.0.0.1:1 failed"`)
}
