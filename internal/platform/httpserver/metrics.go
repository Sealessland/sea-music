package httpserver

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

var httpDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type httpMetricKey struct {
	Method      string
	Route       string
	StatusClass string
}

type httpMetricValue struct {
	Requests uint64
	Errors   uint64
	Sum      float64
	Buckets  []uint64
}

type HTTPMetrics struct {
	mu     sync.RWMutex
	values map[httpMetricKey]*httpMetricValue
}

var defaultHTTPMetrics = &HTTPMetrics{values: make(map[httpMetricKey]*httpMetricValue)}

func recordHTTPRequest(method, route string, status int, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	key := httpMetricKey{Method: method, Route: route, StatusClass: strconv.Itoa(status/100) + "xx"}
	defaultHTTPMetrics.mu.Lock()
	defer defaultHTTPMetrics.mu.Unlock()
	value := defaultHTTPMetrics.values[key]
	if value == nil {
		value = &httpMetricValue{Buckets: make([]uint64, len(httpDurationBuckets))}
		defaultHTTPMetrics.values[key] = value
	}
	value.Requests++
	if status >= 500 {
		value.Errors++
	}
	seconds := duration.Seconds()
	value.Sum += seconds
	for index, boundary := range httpDurationBuckets {
		if seconds <= boundary {
			value.Buckets[index]++
		}
	}
}

func WriteHTTPMetrics(writer io.Writer) {
	defaultHTTPMetrics.mu.RLock()
	defer defaultHTTPMetrics.mu.RUnlock()
	keys := make([]httpMetricKey, 0, len(defaultHTTPMetrics.values))
	for key := range defaultHTTPMetrics.values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].Method+keys[i].Route+keys[i].StatusClass < keys[j].Method+keys[j].Route+keys[j].StatusClass
	})
	for _, key := range keys {
		value := defaultHTTPMetrics.values[key]
		labels := fmt.Sprintf("method=%q,route=%q,status_class=%q", key.Method, key.Route, key.StatusClass)
		_, _ = fmt.Fprintf(writer, "sea_music_http_requests_total{%s} %d\n", labels, value.Requests)
		_, _ = fmt.Fprintf(writer, "sea_music_http_errors_total{%s} %d\n", labels, value.Errors)
		_, _ = fmt.Fprintf(writer, "sea_music_http_request_duration_seconds_sum{%s} %.6f\n", labels, value.Sum)
		for index, boundary := range httpDurationBuckets {
			_, _ = fmt.Fprintf(writer, "sea_music_http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, strconv.FormatFloat(boundary, 'g', -1, 64), value.Buckets[index])
		}
		_, _ = fmt.Fprintf(writer, "sea_music_http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, value.Requests)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.status == 0 {
		recorder.status = status
	}
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(body []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.ResponseWriter.Write(body)
}
