package httpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/platform/httpserver"
)

// TestPanicRecoveryReturnsUnifiedError verifies that a panic yields a sanitized unified 500 response that preserves the client-supplied request ID in both the header and body without exposing panic details.
func TestPanicRecoveryReturnsUnifiedError(t *testing.T) {
	handler := httpserver.NewHandler(discardLogger(), checkerFunc(func(context.Context) error { return nil }), func(router gin.IRouter) {
		router.GET("/panic", func(context *gin.Context) { panic("boom") })
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Header.Set("X-Request-ID", "panic-request-7")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-Request-ID"); got != "panic-request-7" {
		t.Errorf("X-Request-ID = %q, want panic-request-7", got)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Code != "internal_error" {
		t.Errorf("error code = %q, want internal_error", body.Error.Code)
	}
	if body.RequestID != "panic-request-7" {
		t.Errorf("request_id = %q, want panic-request-7", body.RequestID)
	}
	if strings.Contains(response.Body.String(), "boom") {
		t.Errorf("response leaks panic detail: %s", response.Body.String())
	}
}

// TestPanicRecoveryGeneratesRequestID verifies that panic recovery generates a non-empty request ID when none is supplied and returns the same ID in the 500 response header and body.
func TestPanicRecoveryGeneratesRequestID(t *testing.T) {
	handler := httpserver.NewHandler(discardLogger(), checkerFunc(func(context.Context) error { return nil }), func(router gin.IRouter) {
		router.GET("/panic", func(context *gin.Context) { panic("boom") })
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", response.Code, response.Body.String())
	}
	headerID := response.Header().Get("X-Request-ID")
	if headerID == "" {
		t.Fatal("X-Request-ID is empty")
	}
	var body struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.RequestID == "" || body.RequestID != headerID {
		t.Errorf("body request_id = %q, want non-empty and equal to header %q", body.RequestID, headerID)
	}
}
