// Package telemetry wires OpenTelemetry tracing: an OTLP exporter,
// a tracer provider with the service name, and the W3C propagator.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Setup installs the global tracer provider exporting to the OTLP gRPC
// endpoint and returns a shutdown function that flushes pending spans.
// An empty endpoint disables tracing: spans become no-ops and the
// service runs fine without a collector.
//
// Every span is sampled: this is a demo stand where each request is
// interesting. Production would use ParentBased(TraceIDRatioBased).
func Setup(ctx context.Context, serviceName, otlpEndpoint string) (func(context.Context) error, error) {
	// The propagator is global state independent of the provider:
	// with tracing off, Inject simply writes nothing.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if otlpEndpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	// The gRPC connection is lazy; a missing collector surfaces as
	// dropped batches in logs, not as a startup failure.
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}
