package observability

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// UnmatchedRoute labels requests that matched no route, so scanners hitting
// random paths cannot blow up label cardinality.
const UnmatchedRoute = "unmatched"

// Metrics holds the Prometheus registry and the HTTP RED metrics.
type Metrics struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge

	rateLimited     *prometheus.CounterVec
	rateLimitErrors *prometheus.CounterVec
	csrfRejected    *prometheus.CounterVec
}

// NewMetrics creates a dedicated registry with Go runtime, process and build info collectors.
func NewMetrics(version, env string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "http_requests_total",
			Help:      "HTTP requests handled, by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "monthra",
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request latency, by method and route pattern.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		}, []string{"method", "route"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "monthra",
			Name:      "http_requests_in_flight",
			Help:      "HTTP requests currently being served.",
		}),
		rateLimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "ratelimit_rejected_total",
			Help:      "Requests rejected with 429, by rate limit policy.",
		}, []string{"policy"}),
		rateLimitErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "ratelimit_errors_total",
			Help:      "Rate limit checks that failed because Redis was unavailable, by policy.",
		}, []string{"policy"}),
		csrfRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "csrf_rejected_total",
			Help:      "Mutating requests rejected by the CSRF guard, by reason.",
		}, []string{"reason"}),
	}
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace:   "monthra",
		Name:        "build_info",
		Help:        "Build information; the value is always 1.",
		ConstLabels: prometheus.Labels{"version": version, "env": env},
	})
	buildInfo.Set(1)

	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.requests, m.duration, m.inFlight, buildInfo,
		m.rateLimited, m.rateLimitErrors, m.csrfRejected,
	)
	return m
}

// Registry exposes the registry so other packages can register their own collectors.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// RequestStarted increments the in-flight gauge; call the returned func when the request ends.
func (m *Metrics) RequestStarted() (done func()) {
	m.inFlight.Inc()
	return m.inFlight.Dec
}

// ObserveHTTP records one finished request.
func (m *Metrics) ObserveHTTP(method, route string, status int, elapsed time.Duration) {
	if route == "" {
		route = UnmatchedRoute
	}
	m.requests.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
	m.duration.WithLabelValues(method, route).Observe(elapsed.Seconds())
}

// RateLimitRejected counts one request rejected by policy.
func (m *Metrics) RateLimitRejected(policy string) { m.rateLimited.WithLabelValues(policy).Inc() }

// RateLimitError counts one rate limit check that could not reach Redis.
func (m *Metrics) RateLimitError(policy string) { m.rateLimitErrors.WithLabelValues(policy).Inc() }

// CSRFRejected counts one request rejected by the CSRF guard.
func (m *Metrics) CSRFRejected(reason string) { m.csrfRejected.WithLabelValues(reason).Inc() }

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}
