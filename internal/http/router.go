// Package http wires the API routes, middleware chain and servers.
package http

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/NickeAlves/monthra-server/docs"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/http/middleware"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
)

// RouterOptions holds what the router needs from the rest of the app.
type RouterOptions struct {
	Logger       *slog.Logger
	Metrics      *observability.Metrics
	Health       *handler.Health
	MaxBodyBytes int64
	// HSTS adds Strict-Transport-Security (staging and production).
	HSTS bool
	// ServeDocs exposes the OpenAPI document at /docs (never in production).
	ServeDocs bool
}

// NewRouter builds the public API handler.
func NewRouter(opts RouterOptions) http.Handler {
	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		middleware.Locale,
		middleware.SecurityHeaders(opts.HSTS),
		middleware.Instrument(opts.Logger, opts.Metrics),
		middleware.Recoverer(opts.Logger),
		middleware.BodyLimit(opts.MaxBodyBytes),
		chimw.GetHead, // HEAD is answered by the GET route (probes often use HEAD)
	)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, http.StatusNotFound, i18n.ErrNotFound)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, http.StatusMethodNotAllowed, i18n.ErrMethodNotAllowed)
	})

	r.Get("/healthz", opts.Health.Live)
	r.Get("/readyz", opts.Health.Ready)

	if opts.ServeDocs {
		r.Get("/docs", serveOpenAPI)
		r.Get("/docs/openapi.yaml", serveOpenAPI)
	}

	r.Route("/api/v1", func(api chi.Router) {
		// Feature routes (auth, accounts, transactions, ...) are mounted here.
		api.NotFound(func(w http.ResponseWriter, r *http.Request) {
			problem.Write(w, r, http.StatusNotFound, i18n.ErrNotFound)
		})
	})

	// Tracing wraps the whole router so every middleware runs inside the span.
	// Probes are not traced: they would drown real traffic.
	return otelhttp.NewHandler(r, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}),
	)
}

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(docs.OpenAPI)
}
