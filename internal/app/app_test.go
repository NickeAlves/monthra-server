package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newServer(addr string) *http.Server {
	return &http.Server{Addr: addr, Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
}

func TestServeStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, slog.New(slog.DiscardHandler), time.Second, newServer("127.0.0.1:0"), newServer("127.0.0.1:0"))
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("serve did not return after cancel")
	}
}

func TestServeFailsFastWhenAPortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = taken.Close() }()

	free, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	freeAddr := free.Addr().String()
	require.NoError(t, free.Close())

	err = serve(context.Background(), slog.New(slog.DiscardHandler), time.Second,
		newServer(freeAddr), newServer(taken.Addr().String()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen on")

	// The first port was released again, not left serving.
	again, err := net.Listen("tcp", freeAddr)
	require.NoError(t, err)
	_ = again.Close()
}
