package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// TracingOptions configures SetupTracing.
type TracingOptions struct {
	Service string
	Version string
	Env     string
	// Enabled turns on OTLP export. The exporter itself reads the standard
	// OTEL_EXPORTER_OTLP_* variables (endpoint, headers, TLS).
	Enabled bool
}

// SetupTracing installs the W3C propagators and, when enabled, a tracer provider
// that batches spans to the OTLP/HTTP exporter. The returned func flushes and
// stops the provider; it is a no-op when tracing is disabled.
func SetupTracing(ctx context.Context, opts TracingOptions) (shutdown func(context.Context) error, err error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	if !opts.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(
			semconv.ServiceName(opts.Service),
			semconv.ServiceVersion(opts.Version),
			semconv.DeploymentEnvironmentNameKey.String(opts.Env),
		),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("build trace resource: %w", err)
	}
	// The sampler comes from OTEL_TRACES_SAMPLER / OTEL_TRACES_SAMPLER_ARG
	// (default: parent-based, always on).
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
