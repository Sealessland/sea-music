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

func NewRateLimitMiddleware(limiter *ratelimit.Limiter, logger *slog.Logger) *RateLimitMiddleware {
	return &RateLimitMiddleware{limiter: limiter, logger: logger}
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

func RegisterMetricsRoutes(router gin.IRouter) {
	router.GET("/metrics", gin.WrapH(platformmetrics.Handler()))
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
