package appapi

import (
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/httpx"
	platformmetrics "github.com/sealessland/sea-music/internal/platform/metrics"
	"github.com/sealessland/sea-music/internal/platform/ratelimit"
)

type RateLimitMiddleware struct {
	limiter *ratelimit.Limiter
	logger  *slog.Logger
}

// NewRateLimitMiddleware constructs middleware that uses limiter for admission decisions and logger for backend failures.
func NewRateLimitMiddleware(limiter *ratelimit.Limiter, logger *slog.Logger) *RateLimitMiddleware {
	return &RateLimitMiddleware{limiter: limiter, logger: logger}
}

// GinWrap returns Gin middleware that rate-limits requests by class and user-or-IP identity. On allowance it sets the RateLimit-Remaining header and calls the next handler; on denial it sets RateLimit-Remaining and Retry-After headers, writes a 429, and aborts. On backend error it logs the failure and either fails open by calling the next handler or writes a 503 and aborts, depending on failOpen.
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

// RegisterMetricsRoutes registers a GET /metrics endpoint backed by the platform metrics handler.
func RegisterMetricsRoutes(router gin.IRouter) {
	router.GET("/metrics", gin.WrapH(platformmetrics.Handler()))
}

// rateLimitIdentifier returns a stable user-based identifier when an authenticated user ID is available, otherwise an IP-based identifier derived from RemoteAddr with an "unknown" fallback.
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
