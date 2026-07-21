package appapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/appapi"
)

// TestFrontendServesProductShellAndRealAPIClient verifies that registered frontend routes serve the HTML shell, JavaScript API client, and stylesheet with expected content types and markers while unknown paths return 404.
func TestFrontendServesProductShellAndRealAPIClient(t *testing.T) {
	router := gin.New()
	appapi.RegisterFrontendRoutes(router)

	for _, test := range []struct {
		path        string
		contentType string
		contains    []string
	}{
		{path: "/", contentType: "text/html", contains: []string{"Sea Music", "/assets/app.js", "/assets/styles.css"}},
		{path: "/assets/app.js", contentType: "text/javascript", contains: []string{"/api/v1/feed/hot", "/api/v1/sessions", "/api/v1/videos/", `cache: "no-store"`}},
		{path: "/assets/styles.css", contentType: "text/css", contains: []string{".video-grid", "--brand"}},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", test.path, response.Code)
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, test.contentType) {
			t.Fatalf("GET %s Content-Type = %q, want %q", test.path, contentType, test.contentType)
		}
		data, _ := io.ReadAll(response.Body)
		for _, expected := range test.contains {
			if !strings.Contains(string(data), expected) {
				t.Fatalf("GET %s body does not contain %q", test.path, expected)
			}
		}
	}

	unknown := httptest.NewRecorder()
	router.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/not-a-frontend-route", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown frontend route status = %d, want 404", unknown.Code)
	}
}
