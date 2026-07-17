package httpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/platform/httpserver"
)

func TestLivenessDoesNotDependOnReadiness(t *testing.T) {
	handler := httpserver.NewHandler(discardLogger(), checkerFunc(func(context.Context) error {
		return errors.New("database unavailable")
	}))

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("GET /livez status = %d, want 200; body=%s", live.Code, live.Body.String())
	}

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz status = %d, want 503; body=%s", ready.Code, ready.Body.String())
	}
	assertErrorCode(t, ready.Body.Bytes(), "service_unavailable")
}

func TestRequestIDIsPreservedAndReturnedWithErrors(t *testing.T) {
	handler := httpserver.NewHandler(discardLogger(), checkerFunc(func(context.Context) error { return nil }))
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set("X-Request-ID", "client-request-42")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, req)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
	if got := response.Header().Get("X-Request-ID"); got != "client-request-42" {
		t.Errorf("X-Request-ID = %q, want client-request-42", got)
	}
	var body struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.RequestID != "client-request-42" {
		t.Errorf("body request_id = %q, want client-request-42", body.RequestID)
	}
}

func TestRequestIDIsGenerated(t *testing.T) {
	handler := httpserver.NewHandler(discardLogger(), checkerFunc(func(context.Context) error { return nil }))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/livez", nil))

	if got := response.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("X-Request-ID is empty")
	}
}

func TestHTTPMetricsUseBoundedRoutePatternNotResourceID(t *testing.T) {
	handler := httpserver.NewHandler(discardLogger(), checkerFunc(func(context.Context) error { return nil }), func(router gin.IRouter) {
		router.GET("/things/:id", func(context *gin.Context) { context.Status(http.StatusNoContent) })
	})
	for _, id := range []string{"resource-one", "resource-two"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/things/"+id, nil))
	}
	var output strings.Builder
	httpserver.WriteHTTPMetrics(&output)
	if !strings.Contains(output.String(), `route="/things/:id"`) || strings.Contains(output.String(), "resource-one") || strings.Contains(output.String(), "resource-two") {
		t.Fatalf("HTTP metric labels are unbounded: %s", output.String())
	}
}

func TestCORSIsExactAllowlistAndSecurityHeadersAreAlwaysSet(t *testing.T) {
	handler := httpserver.NewHandlerWithOrigins(discardLogger(), checkerFunc(func(context.Context) error { return nil }), []string{"https://app.example.com"})
	allowedRequest := httptest.NewRequest(http.MethodOptions, "/api/v1/videos", nil)
	allowedRequest.Header.Set("Origin", "https://app.example.com")
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, allowedRequest)
	if allowed.Code != http.StatusNoContent || allowed.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("allowed preflight = status %d headers %v", allowed.Code, allowed.Header())
	}
	blockedRequest := httptest.NewRequest(http.MethodGet, "/livez", nil)
	blockedRequest.Header.Set("Origin", "https://evil.example.com")
	blocked := httptest.NewRecorder()
	handler.ServeHTTP(blocked, blockedRequest)
	if blocked.Header().Get("Access-Control-Allow-Origin") != "" || blocked.Header().Get("X-Content-Type-Options") != "nosniff" || blocked.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("blocked origin or security headers = %v", blocked.Header())
	}
}

func assertErrorCode(t *testing.T, data []byte, want string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != want {
		t.Errorf("error code = %q, want %q", body.Error.Code, want)
	}
}

type checkerFunc func(context.Context) error

func (f checkerFunc) Check(ctx context.Context) error { return f(ctx) }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}
