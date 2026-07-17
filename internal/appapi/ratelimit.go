package appapi

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/httpserver"
	"github.com/sealessland/sea-music/internal/platform/httpx"
	"github.com/sealessland/sea-music/internal/platform/ratelimit"
	"github.com/sealessland/sea-music/internal/social"
)

type RateLimitMiddleware struct {
	limiter           *ratelimit.Limiter
	metrics           *ratelimit.Metrics
	logger            *slog.Logger
	eventBacklog      *events.PostgresRepository
	counterReconciler *social.CounterReconciler
	database          *sql.DB
	redis             *redis.Client
}

func (middleware *RateLimitMiddleware) WithRuntimePools(database *sql.DB, client *redis.Client) *RateLimitMiddleware {
	middleware.database = database
	middleware.redis = client
	return middleware
}

func (middleware *RateLimitMiddleware) WithCounterReconciliation(reconciler *social.CounterReconciler) *RateLimitMiddleware {
	middleware.counterReconciler = reconciler
	return middleware
}

func (middleware *RateLimitMiddleware) WithEventBacklog(repository *events.PostgresRepository) *RateLimitMiddleware {
	middleware.eventBacklog = repository
	return middleware
}

func NewRateLimitMiddleware(limiter *ratelimit.Limiter, metrics *ratelimit.Metrics, logger *slog.Logger) *RateLimitMiddleware {
	return &RateLimitMiddleware{limiter: limiter, metrics: metrics, logger: logger}
}

func (middleware *RateLimitMiddleware) Wrap(class string, policy ratelimit.Policy, failOpen bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		identifier := rateLimitIdentifier(request)
		decision, err := middleware.limiter.Allow(request.Context(), "sea:rate:"+class+":"+identifier, class, policy, time.Now())
		if err != nil {
			middleware.logger.ErrorContext(request.Context(), "rate limit backend failed", "class", class, "request_id", httpx.RequestID(request.Context()), "error", err)
			if failOpen {
				next.ServeHTTP(writer, request)
				return
			}
			httpx.WriteError(writer, request, http.StatusServiceUnavailable, "rate_limit_unavailable", "request cannot be admitted")
			return
		}
		writer.Header().Set("RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		if !decision.Allowed {
			writer.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(decision.RetryAfter)))
			httpx.WriteError(writer, request, http.StatusTooManyRequests, "rate_limit_exceeded", "rate limit exceeded")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (middleware *RateLimitMiddleware) GinWrap(class string, policy ratelimit.Policy, failOpen bool) gin.HandlerFunc {
	return func(context *gin.Context) {
		request := context.Request
		identifier := rateLimitIdentifier(request)
		decision, err := middleware.limiter.Allow(request.Context(), "sea:rate:"+class+":"+identifier, class, policy, time.Now())
		if err != nil {
			middleware.logger.ErrorContext(request.Context(), "rate limit backend failed", "class", class, "request_id", httpx.RequestID(request.Context()), "error", err)
			if failOpen {
				context.Next()
				return
			}
			httpx.WriteError(context.Writer, request, http.StatusServiceUnavailable, "rate_limit_unavailable", "request cannot be admitted")
			context.Abort()
			return
		}
		context.Header("RateLimit-Remaining", strconv.Itoa(decision.Remaining))
		if !decision.Allowed {
			context.Header("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(decision.RetryAfter)))
			httpx.WriteError(context.Writer, request, http.StatusTooManyRequests, "rate_limit_exceeded", "rate limit exceeded")
			context.Abort()
			return
		}
		context.Next()
	}
}

func (middleware *RateLimitMiddleware) RegisterRoutes(router gin.IRouter) {
	router.GET("/metrics", ginHandler(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		httpserver.WriteHTTPMetrics(writer)
		classes := middleware.metrics.Snapshot()
		names := make([]string, 0, len(classes))
		for class := range classes {
			names = append(names, class)
		}
		sort.Strings(names)
		for _, class := range names {
			counts := classes[class]
			_, _ = fmt.Fprintf(writer, "sea_music_rate_limit_allowed_total{class=%q} %d\n", class, counts.Allowed)
			_, _ = fmt.Fprintf(writer, "sea_music_rate_limit_rejected_total{class=%q} %d\n", class, counts.Rejected)
			_, _ = fmt.Fprintf(writer, "sea_music_rate_limit_backend_errors_total{class=%q} %d\n", class, counts.BackendErrors)
		}
		if middleware.eventBacklog != nil {
			stats, err := middleware.eventBacklog.Backlog(request.Context())
			if err == nil {
				_, _ = fmt.Fprintf(writer, "sea_music_outbox_events{state=\"pending\"} %d\n", stats.Pending)
				_, _ = fmt.Fprintf(writer, "sea_music_outbox_events{state=\"publishing\"} %d\n", stats.Publishing)
				_, _ = fmt.Fprintf(writer, "sea_music_outbox_events{state=\"failed\"} %d\n", stats.Failed)
				_, _ = fmt.Fprintf(writer, "sea_music_outbox_oldest_seconds %.3f\n", stats.OldestSeconds)
			}
		}
		if middleware.counterReconciler != nil {
			stats, err := middleware.counterReconciler.Stats(request.Context())
			if err == nil {
				_, _ = fmt.Fprintf(writer, "sea_music_counter_reconciliations_total %d\n", stats.Repairs)
				_, _ = fmt.Fprintf(writer, "sea_music_counter_drift_total %d\n", stats.DriftTotal)
			}
		}
		if middleware.database != nil {
			stats := middleware.database.Stats()
			_, _ = fmt.Fprintf(writer, "sea_music_sql_connections{state=\"open\"} %d\n", stats.OpenConnections)
			_, _ = fmt.Fprintf(writer, "sea_music_sql_connections{state=\"in_use\"} %d\n", stats.InUse)
			_, _ = fmt.Fprintf(writer, "sea_music_sql_connections{state=\"idle\"} %d\n", stats.Idle)
			rows, err := middleware.database.QueryContext(request.Context(), `SELECT state, count(*) FROM video.processing_jobs GROUP BY state`)
			if err == nil {
				for rows.Next() {
					var state string
					var count int64
					if rows.Scan(&state, &count) == nil {
						_, _ = fmt.Fprintf(writer, "sea_music_processing_jobs{state=%q} %d\n", state, count)
					}
				}
				rows.Close()
			}
		}
		if middleware.redis != nil {
			stats := middleware.redis.PoolStats()
			_, _ = fmt.Fprintf(writer, "sea_music_redis_connections{state=\"total\"} %d\n", stats.TotalConns)
			_, _ = fmt.Fprintf(writer, "sea_music_redis_connections{state=\"idle\"} %d\n", stats.IdleConns)
			_, _ = fmt.Fprintf(writer, "sea_music_redis_pool_hits_total %d\n", stats.Hits)
			_, _ = fmt.Fprintf(writer, "sea_music_redis_pool_misses_total %d\n", stats.Misses)
			_, _ = fmt.Fprintf(writer, "sea_music_redis_pool_timeouts_total %d\n", stats.Timeouts)
		}
	}))
}

func rateLimitIdentifier(request *http.Request) string {
	if principal, ok := identity.PrincipalFromContext(request.Context()); ok && principal.UserID != "" {
		return "user:" + principal.UserID
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	host = strings.ReplaceAll(host, ":", "_")
	if host == "" {
		host = "unknown"
	}
	return "ip:" + host
}
