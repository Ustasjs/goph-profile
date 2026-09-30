package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestNew(t *testing.T) {
	log, err := New("debug")
	require.NoError(t, err)

	assert.True(t, log.Enabled(context.Background(), slog.LevelDebug))
}

func TestNewLevelFilter(t *testing.T) {
	log, err := New("error")
	require.NoError(t, err)

	assert.False(t, log.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, log.Enabled(context.Background(), slog.LevelError))
}

func TestNewBadLevel(t *testing.T) {
	_, err := New("loud")
	assert.Error(t, err)
}

// record captures one log line written through the trace handler.
func record(ctx context.Context, extra func(*slog.Logger)) map[string]any {
	var buf bytes.Buffer
	log := slog.New(traceHandler{Handler: slog.NewJSONHandler(&buf, nil)})
	if extra != nil {
		extra(log)
	} else {
		log.InfoContext(ctx, "hello", "key", "value")
	}
	var line map[string]any
	_ = json.Unmarshal(buf.Bytes(), &line)
	return line
}

func TestTraceHandlerAddsTraceID(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0xAB},
		SpanID:     trace.SpanID{0xCD},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	line := record(ctx, nil)
	assert.Equal(t, sc.TraceID().String(), line["trace_id"])
	assert.Equal(t, sc.SpanID().String(), line["span_id"])
	assert.Equal(t, "value", line["key"])
}

func TestTraceHandlerSkipsWithoutSpan(t *testing.T) {
	line := record(context.Background(), nil)
	assert.NotContains(t, line, "trace_id")
	assert.NotContains(t, line, "span_id")
}

func TestTraceHandlerSurvivesWith(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01},
		SpanID:     trace.SpanID{0x02},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	// With returns a child logger; the decoration must not be lost.
	line := record(ctx, func(log *slog.Logger) {
		log.With("service", "test").InfoContext(ctx, "hello")
	})
	assert.Equal(t, sc.TraceID().String(), line["trace_id"])
	assert.Equal(t, "test", line["service"])
}
