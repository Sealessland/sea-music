package telemetry

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
)

type Shutdown func(context.Context) error

// Setup installs W3C trace-context and baggage propagation globally and, when endpoint is nonblank, installs an insecure OTLP/gRPC-backed global tracer provider whose returned shutdown function flushes and stops it; a blank endpoint leaves the provider unchanged and returns a no-op shutdown.
func Setup(ctx context.Context, serviceName, endpoint string) (Shutdown, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if strings.TrimSpace(endpoint) == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	serviceResource, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL, semconv.ServiceName(serviceName),
	))
	if err != nil {
		return nil, err
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(serviceResource),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// TraceParent returns the W3C traceparent value for the context's valid span, or falls back to the value produced by the global text-map propagator when no valid span context exists.
func TraceParent(ctx context.Context) string {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		return fmt.Sprintf("00-%s-%s-%02x", spanContext.TraceID(), spanContext.SpanID(), byte(spanContext.TraceFlags()))
	}
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// JoinShutdown returns a shutdown function that invokes each non-nil input in reverse order and joins all returned errors.
func JoinShutdown(functions ...Shutdown) Shutdown {
	return func(ctx context.Context) error {
		var result error
		for index := len(functions) - 1; index >= 0; index-- {
			if functions[index] != nil {
				result = errors.Join(result, functions[index](ctx))
			}
		}
		return result
	}
}
