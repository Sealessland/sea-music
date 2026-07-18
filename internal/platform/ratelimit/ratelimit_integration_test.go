package ratelimit_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/platform/ratelimit"
)

func TestRedisTokenBucketIsAtomicAndReportsRetry(t *testing.T) {
	redisURL := os.Getenv("SEA_REDIS_TEST_URL")
	if redisURL == "" {
		t.Skip("SEA_REDIS_TEST_URL is required for the Redis integration test")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse Redis URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping Redis: %v", err)
	}
	const key = "rate-limit:test:atomic"
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatalf("clear test bucket: %v", err)
	}
	registry := prometheus.NewRegistry()
	metrics := ratelimit.NewMetrics(registry)
	limiter := ratelimit.New(client, metrics)
	policy := ratelimit.Policy{RatePerSecond: 1, Burst: 2}
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	first, err := limiter.Allow(ctx, key, "identity_write", policy, now)
	if err != nil || !first.Allowed {
		t.Fatalf("first Allow() = (%+v, %v)", first, err)
	}
	second, err := limiter.Allow(ctx, key, "identity_write", policy, now)
	if err != nil || !second.Allowed {
		t.Fatalf("second Allow() = (%+v, %v)", second, err)
	}
	third, err := limiter.Allow(ctx, key, "identity_write", policy, now)
	if err != nil || third.Allowed || third.RetryAfter < time.Second {
		t.Fatalf("third Allow() = (%+v, %v), want rejection with retry", third, err)
	}
	afterRefill, err := limiter.Allow(ctx, key, "identity_write", policy, now.Add(time.Second))
	if err != nil || !afterRefill.Allowed {
		t.Fatalf("refilled Allow() = (%+v, %v)", afterRefill, err)
	}

	if err := testutil.GatherAndCompare(registry, strings.NewReader(`# HELP sea_music_rate_limit_allowed_total Total number of requests admitted by the rate limiter.
# TYPE sea_music_rate_limit_allowed_total counter
sea_music_rate_limit_allowed_total{class="identity_write"} 3
# HELP sea_music_rate_limit_rejected_total Total number of requests rejected by the rate limiter.
# TYPE sea_music_rate_limit_rejected_total counter
sea_music_rate_limit_rejected_total{class="identity_write"} 1
`), "sea_music_rate_limit_allowed_total", "sea_music_rate_limit_rejected_total"); err != nil {
		t.Fatalf("gather rate limit metrics: %v", err)
	}
}
