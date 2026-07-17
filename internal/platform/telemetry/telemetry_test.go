package telemetry_test

import (
	"context"
	"testing"

	"github.com/sealessland/sea-music/internal/platform/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
)

func TestTraceParentCarriesCurrentSpanIdentity(t *testing.T) {
	previous := otel.GetTracerProvider()
	provider := trace.NewTracerProvider(trace.WithSampler(trace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	ctx, span := otel.Tracer("test").Start(context.Background(), "operation")
	defer span.End()
	value := telemetry.TraceParent(ctx)
	if len(value) != 55 || value[:3] != "00-" {
		t.Fatalf("TraceParent() = %q", value)
	}
}

func TestSetupWithoutEndpointInstallsNoopSafely(t *testing.T) {
	shutdown, err := telemetry.Setup(context.Background(), "test-service", "")
	if err != nil {
		t.Fatalf("Setup(): %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown(): %v", err)
	}
}
