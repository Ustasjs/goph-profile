package broker

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestHeaderCarrierRoundTrip(t *testing.T) {
	prop := propagation.TraceContext{}

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01},
		SpanID:     trace.SpanID{0x02},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	headers := amqp.Table{}
	prop.Inject(ctx, headerCarrier(headers))
	require.NotEmpty(t, headers["traceparent"])

	got := trace.SpanContextFromContext(prop.Extract(context.Background(), headerCarrier(headers)))
	assert.Equal(t, sc.TraceID(), got.TraceID())
	assert.Equal(t, sc.SpanID(), got.SpanID())
	assert.True(t, got.IsRemote())
}

func TestHeaderCarrierIgnoresNonStringValues(t *testing.T) {
	// AMQP headers hold arbitrary types; the carrier must not panic
	// on them and must report every key.
	c := headerCarrier(amqp.Table{"traceparent": 42, "other": "v"})
	assert.Empty(t, c.Get("traceparent"))
	assert.Equal(t, "v", c.Get("other"))
	assert.ElementsMatch(t, []string{"traceparent", "other"}, c.Keys())
}
