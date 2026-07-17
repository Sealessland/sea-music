package appapi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ginHandler lets the existing thin HTTP handlers run on Gin while keeping
// request.Context, response contracts, and body validation unchanged.
func ginHandler(handler http.HandlerFunc) gin.HandlerFunc {
	return func(context *gin.Context) {
		request := context.Request
		for _, parameter := range context.Params {
			request.SetPathValue(parameter.Key, parameter.Value)
		}
		handler(context.Writer, request)
	}
}
