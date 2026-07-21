package ratelimit

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestMetricsUsePrometheusCounters verifies that recording allowed and rejected requests increments the corresponding Prometheus counters independently for the supplied operation label.
func TestMetricsUsePrometheusCounters(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := NewMetrics(registry)
	metrics.recordAllowed("identity_write")
	metrics.recordAllowed("identity_write")
	metrics.recordRejected("identity_write")

	if got := testutil.ToFloat64(metrics.allowed.WithLabelValues("identity_write")); got != 2 {
		t.Fatalf("allowed counter = %v, want 2", got)
	}
	if got := testutil.ToFloat64(metrics.rejected.WithLabelValues("identity_write")); got != 1 {
		t.Fatalf("rejected counter = %v, want 1", got)
	}
}
