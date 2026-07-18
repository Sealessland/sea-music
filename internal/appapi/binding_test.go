package appapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBindJSONRejectsUnknownFieldsAndMultipleValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`{"name":"sea","extra":true}`,
		`{"name":"sea"} {"name":"music"}`,
	} {
		router := gin.New()
		router.POST("/", func(context *gin.Context) {
			var input struct {
				Name string `json:"name"`
			}
			if err := bindJSON(context, &input); err != nil {
				context.Status(http.StatusBadRequest)
				return
			}
			context.Status(http.StatusNoContent)
		})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, response.Code)
		}
	}
}
