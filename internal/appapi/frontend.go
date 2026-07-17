package appapi

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/index.html web/app.js web/styles.css
var frontendFiles embed.FS

func RegisterFrontendRoutes(router gin.IRouter) {
	serveFrontendFile := func(path, contentType string) http.HandlerFunc {
		return func(writer http.ResponseWriter, _ *http.Request) {
			data, err := frontendFiles.ReadFile(path)
			if err != nil {
				http.Error(writer, "frontend asset unavailable", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", contentType)
			writer.Header().Set("Cache-Control", "no-cache")
			_, _ = writer.Write(data)
		}
	}
	router.GET("/", ginHandler(serveFrontendFile("web/index.html", "text/html; charset=utf-8")))
	router.GET("/assets/app.js", ginHandler(serveFrontendFile("web/app.js", "text/javascript; charset=utf-8")))
	router.GET("/assets/styles.css", ginHandler(serveFrontendFile("web/styles.css", "text/css; charset=utf-8")))
}
