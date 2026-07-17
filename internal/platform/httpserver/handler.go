package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/platform/httpx"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

type ReadinessChecker interface {
	Check(context.Context) error
}

// RouteRegistrar registers application routes on the Gin router.
type RouteRegistrar func(gin.IRouter)

func NewHandler(logger *slog.Logger, readiness ReadinessChecker, registrars ...RouteRegistrar) http.Handler {
	return NewHandlerWithOrigins(logger, readiness, nil, registrars...)
}

func NewHandlerWithOrigins(logger *slog.Logger, readiness ReadinessChecker, allowedOrigins []string, registrars ...RouteRegistrar) http.Handler {
	router := gin.New()
	router.Use(requestLog(logger), recoverPanic(logger), securityHeaders(allowedOrigins))

	router.GET("/livez", func(context *gin.Context) {
		httpx.WriteJSON(context.Writer, http.StatusOK, map[string]string{"status": "ok"})
	})
	router.GET("/readyz", func(context *gin.Context) {
		if err := readiness.Check(context.Request.Context()); err != nil {
			logger.WarnContext(context.Request.Context(), "readiness check failed",
				"request_id", httpx.RequestID(context.Request.Context()),
				"reason", err.Error(),
			)
			httpx.WriteError(context.Writer, context.Request, http.StatusServiceUnavailable, "service_unavailable", "service is not ready")
			return
		}
		httpx.WriteJSON(context.Writer, http.StatusOK, map[string]string{"status": "ready"})
	})
	for _, register := range registrars {
		register(router)
	}
	router.NoRoute(func(context *gin.Context) {
		httpx.WriteError(context.Writer, context.Request, http.StatusNotFound, "not_found", "resource not found")
	})

	// Keep the existing net/http request-id and OTel instrumentation around the
	// Gin engine so downstream services still receive the same context values.
	return httpx.WithRequestID(otelhttp.NewHandler(router, "http.server"))
}

func securityHeaders(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[origin] = true
	}
	return func(context *gin.Context) {
		context.Header("X-Content-Type-Options", "nosniff")
		context.Header("X-Frame-Options", "DENY")
		context.Header("Referrer-Policy", "no-referrer")
		origin := context.GetHeader("Origin")
		if allowed[origin] {
			context.Header("Access-Control-Allow-Origin", origin)
			context.Header("Access-Control-Allow-Credentials", "true")
			context.Header("Vary", "Origin")
			if context.Request.Method == http.MethodOptions {
				context.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				context.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				context.Header("Access-Control-Max-Age", "600")
				context.Status(http.StatusNoContent)
				context.Abort()
				return
			}
		}
		context.Next()
	}
}

func requestLog(logger *slog.Logger) gin.HandlerFunc {
	return func(context *gin.Context) {
		started := time.Now()
		context.Next()
		status := context.Writer.Status()
		if status == 0 {
			status = http.StatusOK
		}
		route := context.FullPath()
		recordHTTPRequest(context.Request.Method, route, status, time.Since(started))
		logger.InfoContext(context.Request.Context(), "http request",
			"request_id", httpx.RequestID(context.Request.Context()),
			"method", context.Request.Method,
			"path", context.Request.URL.Path,
			"route", route,
			"status", status,
			"duration_ms", time.Since(started).Milliseconds(),
			"trace_id", trace.SpanContextFromContext(context.Request.Context()).TraceID().String(),
		)
	}
}

// recoverPanic converts a downstream handler panic into the unified 500 error
// response. It must be registered after requestLog so the request log still
// records the 500, and before every other middleware and route so all
// downstream panics are caught.
func recoverPanic(logger *slog.Logger) gin.HandlerFunc {
	return func(context *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(context.Request.Context(), "http handler panic",
					"request_id", httpx.RequestID(context.Request.Context()),
					"method", context.Request.Method,
					"path", context.Request.URL.Path,
					"error", fmt.Sprint(recovered),
					"stack", string(debug.Stack()),
				)
				context.Abort()
				// A partially written response cannot be rewritten; only send
				// the error model when no headers have gone out yet.
				if context.Writer.Written() {
					return
				}
				httpx.WriteError(context.Writer, context.Request, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		context.Next()
	}
}
