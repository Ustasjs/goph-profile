package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
)

func TestSetupDisabledWithoutEndpoint(t *testing.T) {
	shutdown, err := Setup(context.Background(), "test-service", "")
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	assert.NoError(t, shutdown(context.Background()))

	// The propagator is installed even with tracing off, so header
	// plumbing stays exercised in every configuration.
	assert.Contains(t, otel.GetTextMapPropagator().Fields(), "traceparent")
}

func TestSetupInstallsProvider(t *testing.T) {
	before := otel.GetTracerProvider()
	shutdown, err := Setup(context.Background(), "test-service", "localhost:4317")
	require.NoError(t, err)

	assert.NotSame(t, before, otel.GetTracerProvider())

	// No spans were recorded, so shutdown flushes nothing and must
	// not try to reach the (absent) collector.
	assert.NoError(t, shutdown(context.Background()))
}
