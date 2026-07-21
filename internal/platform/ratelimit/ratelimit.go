package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

var tokenBucket = redis.NewScript(`
local now = tonumber(ARGV[1])
local rate = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])
local current = redis.call('HMGET', KEYS[1], 'tokens', 'updated_at')
local tokens = tonumber(current[1])
local updated_at = tonumber(current[2])
if tokens == nil then tokens = burst end
if updated_at == nil then updated_at = now end
if now < updated_at then now = updated_at end
tokens = math.min(burst, tokens + ((now - updated_at) * rate / 1000))
local allowed = 0
local retry_after = 0
if tokens >= 1 then
  allowed = 1
  tokens = tokens - 1
else
  retry_after = math.ceil((1 - tokens) * 1000 / rate)
end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'updated_at', now)
redis.call('PEXPIRE', KEYS[1], math.max(1000, math.ceil((burst / rate) * 2000)))
return {allowed, retry_after, math.floor(tokens)}
`)

type Policy struct {
	RatePerSecond float64
	Burst         int
}

type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
	Remaining  int
}

type Limiter struct {
	client  redis.UniversalClient
	metrics *Metrics
}

// New returns a limiter that evaluates token-bucket policies through client and reports outcomes to metrics.
func New(client redis.UniversalClient, metrics *Metrics) *Limiter {
	return &Limiter{client: client, metrics: metrics}
}

// Allow atomically evaluates and updates the Redis-backed token bucket for key, records the outcome by class, and reports invalid requests or backend responses as errors.
func (limiter *Limiter) Allow(ctx context.Context, key, class string, policy Policy, now time.Time) (Decision, error) {
	if key == "" || class == "" || policy.RatePerSecond <= 0 || policy.Burst < 1 {
		return Decision{}, errors.New("invalid rate limit request")
	}
	result, err := tokenBucket.Run(ctx, limiter.client, []string{key}, now.UnixMilli(), policy.RatePerSecond, policy.Burst).Slice()
	if err != nil {
		limiter.metrics.recordBackendError(class)
		return Decision{}, fmt.Errorf("evaluate Redis rate limit: %w", err)
	}
	if len(result) != 3 {
		limiter.metrics.recordBackendError(class)
		return Decision{}, fmt.Errorf("unexpected Redis rate limit response length %d", len(result))
	}
	allowed, err := asInt64(result[0])
	if err != nil {
		limiter.metrics.recordBackendError(class)
		return Decision{}, err
	}
	retryMilliseconds, err := asInt64(result[1])
	if err != nil {
		limiter.metrics.recordBackendError(class)
		return Decision{}, err
	}
	remaining, err := asInt64(result[2])
	if err != nil {
		limiter.metrics.recordBackendError(class)
		return Decision{}, err
	}
	decision := Decision{
		Allowed:    allowed == 1,
		RetryAfter: time.Duration(retryMilliseconds) * time.Millisecond,
		Remaining:  int(remaining),
	}
	if decision.Allowed {
		limiter.metrics.recordAllowed(class)
	} else {
		limiter.metrics.recordRejected(class)
	}
	return decision, nil
}

// asInt64 converts a Redis integer represented as an int64 or decimal string, returning an error for malformed strings or unsupported types.
func asInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		var parsed int64
		if _, err := fmt.Sscan(typed, &parsed); err != nil {
			return 0, fmt.Errorf("parse Redis integer %q: %w", typed, err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unexpected Redis integer type %T", value)
	}
}

type Metrics struct {
	allowed       *prometheus.CounterVec
	rejected      *prometheus.CounterVec
	backendErrors *prometheus.CounterVec
}

// NewMetrics creates and registers the rate limiter's allowed, rejected, and backend-error counters, panicking if registration fails.
func NewMetrics(registerer prometheus.Registerer) *Metrics {
	metrics := &Metrics{
		allowed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sea_music_rate_limit_allowed_total",
			Help: "Total number of requests admitted by the rate limiter.",
		}, []string{"class"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sea_music_rate_limit_rejected_total",
			Help: "Total number of requests rejected by the rate limiter.",
		}, []string{"class"}),
		backendErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sea_music_rate_limit_backend_errors_total",
			Help: "Total number of rate limiter backend errors.",
		}, []string{"class"}),
	}
	registerer.MustRegister(metrics.allowed, metrics.rejected, metrics.backendErrors)
	return metrics
}

// recordAllowed increments the allowed-request counter for class.
func (metrics *Metrics) recordAllowed(class string) {
	metrics.allowed.WithLabelValues(class).Inc()
}

// recordRejected increments the rejected-request counter for class.
func (metrics *Metrics) recordRejected(class string) {
	metrics.rejected.WithLabelValues(class).Inc()
}

// recordBackendError increments the backend-error counter for class.
func (metrics *Metrics) recordBackendError(class string) {
	metrics.backendErrors.WithLabelValues(class).Inc()
}

// RetryAfterSeconds rounds duration up to whole seconds and returns at least one second.
func RetryAfterSeconds(duration time.Duration) int {
	return max(1, int(math.Ceil(duration.Seconds())))
}
