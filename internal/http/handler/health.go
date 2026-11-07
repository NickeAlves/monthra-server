// Package handler holds the HTTP handlers of the API.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Pinger is a dependency the readiness probe checks.
type Pinger interface {
	Ping(ctx context.Context) error
}

// PingerFunc adapts a function to Pinger.
type PingerFunc func(ctx context.Context) error

// Ping calls f.
func (f PingerFunc) Ping(ctx context.Context) error { return f(ctx) }

// Health serves the liveness and readiness probes.
type Health struct {
	logger  *slog.Logger
	checks  map[string]Pinger
	timeout time.Duration
}

// NewHealth returns probes that check every dependency in checks within timeout.
func NewHealth(logger *slog.Logger, timeout time.Duration, checks map[string]Pinger) *Health {
	return &Health{logger: logger, checks: checks, timeout: timeout}
}

type statusResponse struct {
	Status string `json:"status"`
}

// Live reports that the process is up. It never touches dependencies, so a
// database outage does not make the orchestrator restart healthy API instances.
func (h *Health) Live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// Ready reports whether every dependency answers. Failures are logged with the
// dependency name; the response does not say which one failed.
func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		failed bool
	)
	for name, check := range h.checks {
		wg.Go(func() {
			if err := check.Ping(ctx); err != nil {
				h.logger.WarnContext(r.Context(), "readiness check failed",
					slog.String("dependency", name), slog.String("error", err.Error()))
				mu.Lock()
				failed = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if failed {
		problem.Write(w, r, http.StatusServiceUnavailable, i18n.ErrServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
