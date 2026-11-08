// Package http wires the API routes, middleware chain and servers.
package http

import (
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/NickeAlves/monthra-server/docs"
	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/csrf"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/http/middleware"
	"github.com/NickeAlves/monthra-server/internal/http/problem"
	"github.com/NickeAlves/monthra-server/internal/i18n"
	"github.com/NickeAlves/monthra-server/internal/observability"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
)

// PolicyAPIByIP is the baseline budget of every /api/v1 request, by client IP.
const PolicyAPIByIP = "api_ip"

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

	// AllowedOrigins are the normalized web origins allowed from browsers.
	AllowedOrigins []string
	// TrustedProxies are the proxies whose X-Forwarded-For is believed.
	TrustedProxies []netip.Prefix
	// Cookies names the cookies and sets their Secure flag.
	Cookies    cookie.Policy
	CSRFSigner csrf.Signer

	Limiter ratelimit.Limiter
	Keys    cache.Keys
	// APIRateByIP is the baseline budget of /api/v1 per client IP.
	APIRateByIP config.Rate
}

// NewRouter builds the public API handler.
func NewRouter(opts RouterOptions) http.Handler {
	allowed := middleware.NewOriginAllowlist(opts.AllowedOrigins)

	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		middleware.ClientIP(opts.TrustedProxies),
		middleware.Locale,
		middleware.SecurityHeaders(opts.HSTS),
		middleware.Instrument(opts.Logger, opts.Metrics),
		middleware.Recoverer(opts.Logger),
		// CORS runs before the rate limit and the CSRF guard, so browsers can
		// read their 429 and 403 answers.
		middleware.CORS(allowed),
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
		api.Use(
			middleware.RateLimit(middleware.RateLimitOptions{
				Limiter: opts.Limiter,
				Keys:    opts.Keys,
				Policy:  ratelimit.Policy{Name: PolicyAPIByIP, Rate: opts.APIRateByIP, FailOpen: true},
				Subject: middleware.ByClientIP,
				Logger:  opts.Logger,
				Metrics: opts.Metrics,
			}),
			middleware.CSRF(middleware.CSRFOptions{
				Signer:  opts.CSRFSigner,
				Cookies: opts.Cookies,
				Allowed: allowed,
				Metrics: opts.Metrics,
			}),
		)
		api.Method(http.MethodGet, "/auth/csrf", handler.CSRFToken{Signer: opts.CSRFSigner, Cookies: opts.Cookies})
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
