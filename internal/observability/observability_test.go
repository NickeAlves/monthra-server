package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line))
	return line
}

func TestLoggerRedactsSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, LoggerOptions{Level: slog.LevelInfo, Service: "svc", Version: "v1", Env: "local"})

	logger.Info("login",
		slog.String("password", "hunter2"),
		slog.String("refresh_token", "abc"),
		slog.String("Authorization", "Bearer x"),
		slog.String("user_cpf", "123"),
		slog.Group("req", slog.String("cookie", "session=1")),
		slog.String("email_domain", "example.com"),
	)

	line := decodeLine(t, &buf)
	assert.Equal(t, Redacted, line["password"])
	assert.Equal(t, Redacted, line["refresh_token"])
	assert.Equal(t, Redacted, line["Authorization"])
	assert.Equal(t, Redacted, line["user_cpf"])
	assert.Equal(t, Redacted, line["req"].(map[string]any)["cookie"])
	assert.Equal(t, "example.com", line["email_domain"])
	assert.Equal(t, "svc", line["service"])
	assert.Equal(t, "v1", line["version"])
	assert.Equal(t, "local", line["env"])
	assert.NotContains(t, buf.String(), "hunter2")
}

func TestLoggerAddsCorrelationFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, LoggerOptions{Level: slog.LevelInfo})

	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	meta := &RequestMeta{RequestID: "req-1"}
	ctx = WithRequestMeta(ctx, meta)
	meta.UserID = "user-42" // filled later by auth middleware

	logger.With(slog.String("component", "test")).InfoContext(ctx, "hello")

	line := decodeLine(t, &buf)
	assert.Equal(t, "req-1", line["request_id"])
	assert.Equal(t, "user-42", line["user_id"])
	assert.Equal(t, traceID.String(), line["trace_id"])
	assert.Equal(t, spanID.String(), line["span_id"])
	assert.Equal(t, "test", line["component"])
}

func TestLoggerWithGroupKeepsCorrelation(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, LoggerOptions{Level: slog.LevelInfo}).WithGroup("g")
	ctx := WithRequestMeta(context.Background(), &RequestMeta{RequestID: "req-2"})

	logger.InfoContext(ctx, "grouped", slog.Int("n", 1))

	assert.Contains(t, buf.String(), `"request_id":"req-2"`)
}

func TestRequestIDFromEmptyContext(t *testing.T) {
	assert.Empty(t, RequestIDFrom(context.Background()))
	assert.Nil(t, RequestMetaFrom(context.Background()))
}

func TestMetricsExposeHTTPSeries(t *testing.T) {
	m := NewMetrics("v1.2.3", "local")
	done := m.RequestStarted()
	m.ObserveHTTP(http.MethodGet, "/healthz", http.StatusOK, 15*time.Millisecond)
	m.ObserveHTTP(http.MethodGet, "", http.StatusNotFound, time.Millisecond)
	m.TransactionCreated("expense")
	m.TransactionCreated("expense")
	m.CacheRequest("summary", "hit")
	m.CacheError("summary", "invalidate")
	done()

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, body, `monthra_http_requests_total{method="GET",route="/healthz",status="200"} 1`)
	assert.Contains(t, body, `monthra_http_requests_total{method="GET",route="unmatched",status="404"} 1`)
	assert.Contains(t, body, `monthra_http_request_duration_seconds_bucket{method="GET",route="/healthz",le="0.025"} 1`)
	assert.Contains(t, body, `monthra_http_requests_in_flight 0`)
	assert.Contains(t, body, `monthra_build_info{env="local",version="v1.2.3"} 1`)
	assert.Contains(t, body, `monthra_transactions_created_total{kind="expense"} 2`)
	assert.Contains(t, body, `monthra_cache_requests_total{cache="summary",result="hit"} 1`)
	assert.Contains(t, body, `monthra_cache_errors_total{cache="summary",op="invalidate"} 1`)
	assert.True(t, strings.Contains(body, "go_goroutines"), "go runtime collector registered")
	assert.NotNil(t, m.Registry())
}

func TestSetupTracingDisabledIsNoop(t *testing.T) {
	shutdown, err := SetupTracing(context.Background(), TracingOptions{Enabled: false})
	require.NoError(t, err)
	assert.NoError(t, shutdown(context.Background()))
}

func TestSetupTracingEnabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	shutdown, err := SetupTracing(context.Background(), TracingOptions{
		Service: "monthra-server", Version: "test", Env: "local", Enabled: true,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	assert.NoError(t, shutdown(ctx))
}
