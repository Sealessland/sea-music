package appapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGinHandlerCopiesRouteParametersToNetHTTPRequest(t *testing.T) {
	router := gin.New()
	router.GET("/videos/:video_id", ginHandler(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.PathValue("video_id"); got != "video-42" {
			t.Fatalf("PathValue(video_id) = %q, want video-42", got)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/videos/video-42", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.Code)
	}
}
