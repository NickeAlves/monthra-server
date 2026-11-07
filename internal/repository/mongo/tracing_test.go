package mongo

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func rawCommand(t *testing.T, doc bson.D) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(doc)
	require.NoError(t, err)
	return raw
}

func TestCommandMonitorRecordsSpans(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	monitor := newCommandMonitor(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	ctx := context.Background()

	monitor.Started(ctx, &event.CommandStartedEvent{
		Command:      rawCommand(t, bson.D{{Key: "find", Value: "transactions"}, {Key: "filter", Value: bson.D{{Key: "description", Value: "salary"}}}}),
		DatabaseName: "monthra",
		CommandName:  "find",
		RequestID:    1,
	})
	monitor.Succeeded(ctx, &event.CommandSucceededEvent{CommandFinishedEvent: event.CommandFinishedEvent{RequestID: 1}})

	monitor.Started(ctx, &event.CommandStartedEvent{
		Command:      rawCommand(t, bson.D{{Key: "ping", Value: 1}}),
		DatabaseName: "admin",
		CommandName:  "ping",
		RequestID:    2,
	})
	monitor.Failed(ctx, &event.CommandFailedEvent{
		CommandFinishedEvent: event.CommandFinishedEvent{RequestID: 2},
		Failure:              errors.New("auth failed"),
	})

	// An unknown request id is ignored.
	monitor.Succeeded(ctx, &event.CommandSucceededEvent{CommandFinishedEvent: event.CommandFinishedEvent{RequestID: 99}})

	spans := recorder.Ended()
	require.Len(t, spans, 2)

	find := spans[0]
	assert.Equal(t, "find transactions", find.Name())
	assert.Contains(t, find.Attributes(), attribute.String("db.collection.name", "transactions"))
	assert.Contains(t, find.Attributes(), attribute.String("db.namespace", "monthra"))
	for _, kv := range find.Attributes() {
		assert.NotContains(t, kv.Value.String(), "salary", "query values never become attributes")
	}

	ping := spans[1]
	assert.Equal(t, "ping", ping.Name())
	assert.Equal(t, codes.Error, ping.Status().Code)
	assert.Equal(t, "auth failed", ping.Status().Description)
}
