//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	apihttp "github.com/NickeAlves/monthra-server/internal/http"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/observability"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/internal/testutil"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func waitHealthy(t *testing.T, base string) {
	t.Helper()
	require.Eventually(t, func() bool {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 20*time.Second, 100*time.Millisecond)
}

// TestRunServesAndShutsDown boots the whole service against real Mongo and Redis.
func TestRunServesAndShutsDown(t *testing.T) {
	_, redisAddr := testutil.StartRedis(t)
	t.Setenv("MONGO_URI", testutil.StartMongo(t))
	t.Setenv("REDIS_ADDR", redisAddr)
	t.Setenv("CSRF_SECRET", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)))
	t.Setenv("JWT_SIGNING_KEYS", "k1:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32)))
	t.Setenv("JWT_ACTIVE_KID", "k1")
	t.Setenv("ENCRYPTION_KEYS", "e1:"+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x25}, 32)))
	t.Setenv("ENCRYPTION_ACTIVE_KID", "e1")
	t.Setenv("SMTP_HOST", "127.0.0.1")
	t.Setenv("SMTP_FROM", "no-reply@monthra.example")
	t.Setenv("HTTP_ADDR", freeAddr(t))
	t.Setenv("METRICS_ADDR", freeAddr(t))
	cfg, err := config.Load()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, slog.New(slog.DiscardHandler), "test") }()

	api := "http://" + cfg.HTTP.Addr
	waitHealthy(t, api)

	status, body := get(t, api+"/readyz")
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"status":"ok"}`, body)

	status, _ = get(t, api+"/docs/openapi.yaml")
	assert.Equal(t, http.StatusOK, status, "docs are served outside production")

	status, _ = get(t, api+"/metrics")
	assert.Equal(t, http.StatusNotFound, status, "/metrics is not on the public port")

	status, body = get(t, "http://"+cfg.HTTP.MetricsAddr+"/metrics")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `monthra_http_requests_total{method="GET",route="/readyz",status="200"}`)

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestReadyReportsRedisOutage stops Redis under a running router.
func TestReadyReportsRedisOutage(t *testing.T) {
	ctx := context.Background()
	redisC, redisAddr := testutil.StartRedis(t)

	mongoClient, err := repomongo.Connect(ctx, config.Mongo{URI: testutil.StartMongo(t), ConnectTimeout: 10 * time.Second, MaxPoolSize: 5}, "test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mongoClient.Disconnect(ctx) })
	redisClient, err := cache.Connect(ctx, config.Redis{Addr: redisAddr})
	require.NoError(t, err)
	t.Cleanup(func() { _ = redisClient.Close() })

	logger := slog.New(slog.DiscardHandler)
	router := apihttp.NewRouter(apihttp.RouterOptions{
		Logger:  logger,
		Metrics: observability.NewMetrics("test", "local"),
		Health: handler.NewHealth(logger, readinessTimeout, map[string]handler.Pinger{
			"mongo": repomongo.Pinger{Client: mongoClient},
			"redis": cache.Pinger{Client: redisClient},
		}),
		MaxBodyBytes: 1 << 20,
	})
	srv := &http.Server{Addr: freeAddr(t), Handler: router, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() { _ = srv.Close() })
	base := "http://" + srv.Addr
	waitHealthy(t, base)

	status, _ := get(t, base+"/readyz")
	require.Equal(t, http.StatusOK, status)

	stopTimeout := 5 * time.Second
	require.NoError(t, redisC.Stop(ctx, &stopTimeout))

	status, body := get(t, base+"/readyz")
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Contains(t, body, `"code":"service_unavailable"`)

	status, _ = get(t, base+"/healthz")
	assert.Equal(t, http.StatusOK, status, "liveness ignores dependencies")
}

func TestConnectFailsFastWithBadCredentials(t *testing.T) {
	_, err := cache.Connect(context.Background(), config.Redis{Addr: "127.0.0.1:1"})
	assert.ErrorContains(t, err, "ping redis")
}
