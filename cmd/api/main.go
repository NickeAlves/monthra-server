// Command api runs the Monthra API.
//
//	api              run the service
//	api healthcheck  probe the local /healthz (used by the distroless image's HEALTHCHECK)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/NickeAlves/monthra-server/internal/app"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	os.Exit(run())
}

func run() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid configuration:", err)
		return 2
	}
	logger := observability.NewLogger(os.Stdout, observability.LoggerOptions{
		Level:   cfg.LogLevel,
		Service: cfg.ServiceName,
		Version: version,
		Env:     string(cfg.AppEnv),
	})
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting")
	if err := app.Run(ctx, cfg, logger, version); err != nil {
		logger.Error("stopped with error", slog.String("error", err.Error()))
		return 1
	}
	logger.Info("stopped")
	return 0
}

// healthcheck exits 0 when the local API answers /healthz with 200.
func healthcheck() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck: invalid HTTP_ADDR:", err)
		return 1
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprintln(os.Stderr, "healthcheck: invalid port in HTTP_ADDR")
		return 1
	}
	// Always loopback: only the port comes from the environment.
	target := url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: "/healthz"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
