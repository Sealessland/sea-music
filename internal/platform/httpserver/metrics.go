package httpserver

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	platformmetrics "github.com/sealessland/sea-music/internal/platform/metrics"
)

type HTTPMetrics struct {
	requests *prometheus.CounterVec
	errors   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

var defaultHTTPMetrics = newHTTPMetrics(platformmetrics.Registry)

// newHTTPMetrics creates and registers HTTP request count, 5xx response count, and request-duration histogram collectors labeled by method, route, and status_class, panicking if registration fails.
func newHTTPMetrics(registerer prometheus.Registerer) *HTTPMetrics {
	labels := []string{"method", "route", "status_class"}
	metrics := &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sea_music_http_requests_total",
			Help: "Total number of HTTP requests.",
		}, labels),
		errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sea_music_http_errors_total",
			Help: "Total number of HTTP requests returning a 5xx response.",
		}, labels),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sea_music_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, labels),
	}
	registerer.MustRegister(metrics.requests, metrics.errors, metrics.duration)
	return metrics
}

// recordHTTPRequest records a request against the default collectors, using "unmatched" for an empty route, incrementing the error counter for 5xx statuses, and always observing the request duration in the histogram.
func recordHTTPRequest(method, route string, status int, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	statusClass := strconv.Itoa(status/100) + "xx"
	labels := []string{method, route, statusClass}
	defaultHTTPMetrics.requests.WithLabelValues(labels...).Inc()
	if status >= http.StatusInternalServerError {
		defaultHTTPMetrics.errors.WithLabelValues(labels...).Inc()
	}
	defaultHTTPMetrics.duration.WithLabelValues(labels...).Observe(duration.Seconds())
}
