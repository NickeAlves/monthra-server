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

	authLogins     *prometheus.CounterVec
	refreshReuse   prometheus.Counter
	passwordResets prometheus.Counter
	mfaEvents      *prometheus.CounterVec
	emailSent      *prometheus.CounterVec
	emailDropped   *prometheus.CounterVec

	transactionsCreated *prometheus.CounterVec

	cacheRequests *prometheus.CounterVec
	cacheErrors   *prometheus.CounterVec

	budgetAlerts      *prometheus.CounterVec
	budgetAlertErrors prometheus.Counter
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
		authLogins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "auth_logins_total",
			Help:      "Login attempts, by result (success, failure, locked, unverified, captcha, mfa_required, mfa_failure).",
		}, []string{"result"}),
		refreshReuse: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "auth_refresh_reuse_total",
			Help:      "Refresh tokens presented again after rotation; each one revoked its session.",
		}),
		passwordResets: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "auth_password_resets_total",
			Help:      "Passwords changed through an emailed reset link.",
		}),
		mfaEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "auth_mfa_total",
			Help:      "Two-step verification changes and recovery code sign-ins, by event (enabled, disabled, recovery_code_used).",
		}, []string{"event"}),
		emailSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "email_sent_total",
			Help:      "Transactional emails handled, by template and result (sent, failed, render_error).",
		}, []string{"template", "result"}),
		emailDropped: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "email_dropped_total",
			Help:      "Transactional emails dropped before delivery because the queue was full or closed, by template.",
		}, []string{"template"}),
		transactionsCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "transactions_created_total",
			Help:      "Transactions created, by kind (expense, income, transfer).",
		}, []string{"kind"}),
		cacheRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "cache_requests_total",
			Help:      "Cache lookups, by cache and result (hit, miss).",
		}, []string{"cache", "result"}),
		cacheErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "cache_errors_total",
			Help:      "Cache operations that failed (the request went on without the cache), by cache and operation (version, get, set, invalidate).",
		}, []string{"cache", "op"}),
		budgetAlerts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "budget_alerts_total",
			Help:      "Budget alerts given, by level (warning, exceeded) and result (sent, muted by the owner's preference).",
		}, []string{"level", "result"}),
		budgetAlertErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "monthra",
			Name:      "budget_alert_errors_total",
			Help:      "Budget alert checks that failed after a write (the write went on; the alert waits for the next check).",
		}),
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
		m.authLogins, m.refreshReuse, m.passwordResets, m.mfaEvents, m.emailSent, m.emailDropped,
		m.transactionsCreated, m.cacheRequests, m.cacheErrors, m.budgetAlerts, m.budgetAlertErrors,
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

// Login counts one login attempt by result.
func (m *Metrics) Login(result string) { m.authLogins.WithLabelValues(result).Inc() }

// RefreshReuse counts one detected refresh token reuse.
func (m *Metrics) RefreshReuse() { m.refreshReuse.Inc() }

// PasswordReset counts a password changed through a reset link.
func (m *Metrics) PasswordReset() { m.passwordResets.Inc() }

// MFA counts a two-step verification event.
func (m *Metrics) MFA(event string) { m.mfaEvents.WithLabelValues(event).Inc() }

// EmailSent counts one email outcome.
func (m *Metrics) EmailSent(template, result string) {
	m.emailSent.WithLabelValues(template, result).Inc()
}

// EmailDropped counts one email dropped before delivery.
func (m *Metrics) EmailDropped(template string) { m.emailDropped.WithLabelValues(template).Inc() }

// TransactionCreated counts one transaction created, by kind.
func (m *Metrics) TransactionCreated(kind string) { m.transactionsCreated.WithLabelValues(kind).Inc() }

// CacheRequest counts one cache lookup, by result (hit, miss).
func (m *Metrics) CacheRequest(cache, result string) {
	m.cacheRequests.WithLabelValues(cache, result).Inc()
}

// CacheError counts one failed cache operation.
func (m *Metrics) CacheError(cache, op string) { m.cacheErrors.WithLabelValues(cache, op).Inc() }

// BudgetAlert counts one budget alert given, by level and result.
func (m *Metrics) BudgetAlert(level, result string) {
	m.budgetAlerts.WithLabelValues(level, result).Inc()
}

// BudgetAlertError counts one failed budget alert check.
func (m *Metrics) BudgetAlertError() { m.budgetAlertErrors.Inc() }

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}
