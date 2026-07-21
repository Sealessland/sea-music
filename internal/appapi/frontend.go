package appapi

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/index.html web/app.js web/styles.css
var frontendFiles embed.FS

// RegisterFrontendRoutes adds handlers for the embedded index, JavaScript, and stylesheet assets, serving them without caching and returning HTTP 500 if an asset cannot be read.
func RegisterFrontendRoutes(router gin.IRouter) {
	serveFrontendFile := func(path, contentType string) gin.HandlerFunc {
		return func(context *gin.Context) {
			data, err := frontendFiles.ReadFile(path)
			if err != nil {
				context.String(http.StatusInternalServerError, "frontend asset unavailable")
				return
			}
			context.Header("Content-Type", contentType)
			context.Header("Cache-Control", "no-cache")
			context.Data(http.StatusOK, contentType, data)
		}
	}
	router.GET("/", serveFrontendFile("web/index.html", "text/html; charset=utf-8"))
	router.GET("/assets/app.js", serveFrontendFile("web/app.js", "text/javascript; charset=utf-8"))
	router.GET("/assets/styles.css", serveFrontendFile("web/styles.css", "text/css; charset=utf-8"))
}
