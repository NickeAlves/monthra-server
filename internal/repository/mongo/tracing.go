package mongo

import (
	"context"
	"sync"

	"go.mongodb.org/mongo-driver/v2/event"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// The contrib otelmongo package for driver v2 has no tagged release yet, so
// this small command monitor creates the client spans instead. It records the
// operation and collection only; query values never become span attributes.

const tracerName = "github.com/NickeAlves/monthra-server/internal/repository/mongo"

type commandTracer struct {
	tracer trace.Tracer
	spans  sync.Map // request id (int64) -> trace.Span
}

// NewCommandMonitor returns a monitor that traces every MongoDB command with
// the global tracer provider.
func NewCommandMonitor() *event.CommandMonitor {
	return newCommandMonitor(otel.GetTracerProvider())
}

func newCommandMonitor(tp trace.TracerProvider) *event.CommandMonitor {
	ct := &commandTracer{tracer: tp.Tracer(tracerName)}
	return &event.CommandMonitor{
		Started:   ct.started,
		Succeeded: func(_ context.Context, e *event.CommandSucceededEvent) { ct.finish(e.RequestID, nil) },
		Failed:    func(_ context.Context, e *event.CommandFailedEvent) { ct.finish(e.RequestID, e.Failure) },
	}
}

func (ct *commandTracer) started(ctx context.Context, e *event.CommandStartedEvent) {
	attrs := []attribute.KeyValue{
		semconv.DBSystemNameMongoDB,
		semconv.DBNamespace(e.DatabaseName),
		semconv.DBOperationName(e.CommandName),
	}
	name := e.CommandName
	if coll := collectionName(e); coll != "" {
		attrs = append(attrs, semconv.DBCollectionName(coll))
		name += " " + coll
	}
	_, span := ct.tracer.Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
	ct.spans.Store(e.RequestID, span)
}

func (ct *commandTracer) finish(requestID int64, failure error) {
	v, ok := ct.spans.LoadAndDelete(requestID)
	if !ok {
		return
	}
	span, _ := v.(trace.Span)
	if span == nil {
		return
	}
	if failure != nil {
		span.SetStatus(codes.Error, failure.Error())
	}
	span.End()
}

// collectionName reads the collection from the first element of the command
// document ({find: "transactions", ...}); commands without one return "".
func collectionName(e *event.CommandStartedEvent) string {
	elems, err := e.Command.Elements()
	if err != nil || len(elems) == 0 {
		return ""
	}
	if v, ok := elems[0].Value().StringValueOK(); ok && elems[0].Key() == e.CommandName {
		return v
	}
	return ""
}
