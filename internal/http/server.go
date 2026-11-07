package http

import (
	"log/slog"
	"net/http"

	"github.com/NickeAlves/monthra-server/internal/config"
)

// maxHeaderBytes bounds request headers; cookies plus a JWT fit comfortably.
const maxHeaderBytes = 64 << 10

// NewServer returns an http.Server with the timeouts from cfg, so slow or
// stalled clients cannot hold connections open indefinitely.
func NewServer(addr string, h http.Handler, cfg config.HTTP, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// NewMetricsHandler serves only /metrics; it listens on an internal port.
func NewMetricsHandler(metrics http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics)
	return mux
}
