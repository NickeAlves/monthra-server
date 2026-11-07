package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/NickeAlves/monthra-server/internal/observability"
)

// quietPaths are probed constantly by orchestrators; their access logs drop to debug.
var quietPaths = map[string]bool{"/healthz": true, "/readyz": true}

// Instrument records RED metrics by chi route pattern, names the current span
// after the route and writes one structured access log line per request.
//
// It logs the path without the query string (queries may carry tokens) and
// leaves out the client IP and user agent, which are personal data under LGPD.
func Instrument(logger *slog.Logger, metrics *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			done := metrics.RequestStarted()
			defer done()

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK // handler wrote nothing: net/http sends 200
			}
			elapsed := time.Since(start)

			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}
			metrics.ObserveHTTP(r.Method, route, status, elapsed)

			if route != "" {
				span := trace.SpanFromContext(r.Context())
				span.SetName(r.Method + " " + route)
				span.SetAttributes(attribute.String("http.route", route))
			}

			level := slog.LevelInfo
			switch {
			case status >= http.StatusInternalServerError:
				level = slog.LevelError
			case quietPaths[r.URL.Path]:
				level = slog.LevelDebug
			}
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", route),
				slog.Int("status", status),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Float64("duration_ms", float64(elapsed.Microseconds())/1000),
			)
		})
	}
}
