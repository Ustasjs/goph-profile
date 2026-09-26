package telemetry

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
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

// startRecordedSpan gives a real span whose final state the test can
// inspect after End.
func startRecordedSpan(t *testing.T) (trace.Span, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	_, span := tp.Tracer("test").Start(context.Background(), "op")
	return span, rec
}

func TestEndMarksRealError(t *testing.T) {
	span, rec := startRecordedSpan(t)
	boom := errors.New("boom")

	End(span, boom)

	got := rec.Ended()[0]
	assert.Equal(t, codes.Error, got.Status().Code)
	assert.Equal(t, "boom", got.Status().Description)
	require.Len(t, got.Events(), 1)
}

func TestEndSkipsExpectedError(t *testing.T) {
	span, rec := startRecordedSpan(t)
	notFound := errors.New("not found")

	End(span, fmt.Errorf("wrap: %w", notFound), notFound)

	got := rec.Ended()[0]
	assert.Equal(t, codes.Unset, got.Status().Code)
	assert.Empty(t, got.Events())
}

func TestEndSkipsNilError(t *testing.T) {
	span, rec := startRecordedSpan(t)

	End(span, nil)

	got := rec.Ended()[0]
	assert.Equal(t, codes.Unset, got.Status().Code)
	assert.Empty(t, got.Events())
}
