package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-contrib/cors"
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

// NewHandler constructs the instrumented Gin HTTP handler with request IDs, logging, panic recovery, security headers, health endpoints, registered application routes, and JSON 404 responses, without enabling CORS.
func NewHandler(logger *slog.Logger, readiness ReadinessChecker, registrars ...RouteRegistrar) http.Handler {
	return NewHandlerWithOrigins(logger, readiness, nil, registrars...)
}

// NewHandlerWithOrigins constructs the instrumented Gin HTTP handler, registers health and application routes, and enables credentialed CORS only when allowedOrigins is non-empty; readiness failures are logged and returned as 503 responses.
func NewHandlerWithOrigins(logger *slog.Logger, readiness ReadinessChecker, allowedOrigins []string, registrars ...RouteRegistrar) http.Handler {
	router := gin.New()
	router.Use(requestLog(logger), recoverPanic(logger), securityHeaders())
	if len(allowedOrigins) > 0 {
		router.Use(cors.New(cors.Config{
			AllowOrigins:     allowedOrigins,
			AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
			AllowHeaders:     []string{"Authorization", "Content-Type", "X-Request-ID"},
			AllowCredentials: true,
			MaxAge:           10 * time.Minute,
		}))
	}

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

// securityHeaders adds content-sniffing, framing, and referrer-policy protections to every response before continuing the middleware chain.
func securityHeaders() gin.HandlerFunc {
	return func(context *gin.Context) {
		context.Header("X-Content-Type-Options", "nosniff")
		context.Header("X-Frame-Options", "DENY")
		context.Header("Referrer-Policy", "no-referrer")
		context.Next()
	}
}

// requestLog records metrics and emits a structured completion log for each request, including method, path, matched route, status (defaulting to 200 when unset), duration, request ID, and trace ID. Note that duration is computed independently for the metrics and log calls.
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
