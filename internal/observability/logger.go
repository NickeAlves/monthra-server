package observability

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// Redacted replaces the value of any attribute whose key looks sensitive.
const Redacted = "[REDACTED]"

// sensitiveKeyParts are matched case-insensitively against attribute keys.
// Matching on substrings catches variants such as password_hash, csrf_token or mfa_secret.
var sensitiveKeyParts = []string{
	"password", "passwd", "secret", "token", "authorization", "cookie",
	"api_key", "apikey", "cpf", "credential", "private_key",
}

// LoggerOptions configures NewLogger.
type LoggerOptions struct {
	Level   slog.Leveler
	Service string
	Version string
	Env     string
}

// NewLogger returns a JSON logger that adds request/trace correlation fields
// from the context and redacts sensitive attributes.
func NewLogger(w io.Writer, opts LoggerOptions) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       opts.Level,
		ReplaceAttr: redact,
	})
	return slog.New(&contextHandler{Handler: handler}).With(
		slog.String("service", opts.Service),
		slog.String("version", opts.Version),
		slog.String("env", opts.Env),
	)
}

func redact(_ []string, a slog.Attr) slog.Attr {
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	return a
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// contextHandler enriches every record with request_id, user_id, trace_id and span_id.
type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if meta := RequestMetaFrom(ctx); meta != nil {
		if meta.RequestID != "" {
			r.AddAttrs(slog.String("request_id", meta.RequestID))
		}
		if meta.UserID != "" {
			r.AddAttrs(slog.String("user_id", meta.UserID))
		}
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}
