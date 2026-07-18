package appapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/discovery"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/httpx"
)

type DiscoveryHandler struct {
	repository *discovery.PostgresRepository
	auth       *Authenticator
	logger     *slog.Logger
}

func NewDiscoveryHandler(repository *discovery.PostgresRepository, auth *Authenticator, logger *slog.Logger) *DiscoveryHandler {
	return &DiscoveryHandler{repository: repository, auth: auth, logger: logger}
}

func (handler *DiscoveryHandler) RegisterRoutes(router gin.IRouter) {
	router.GET("/api/v1/feed/following", handler.auth.Require(), handler.following)
	router.GET("/api/v1/feed/hot", handler.auth.Optional(), handler.hot)
	router.GET("/api/v1/feed/recommendations", handler.auth.Require(), handler.recommendations)
}

func (handler *DiscoveryHandler) following(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	page, err := handler.repository.Following(request.Context(), principal.UserID, context.Query("cursor"), parseQueryInt(context, "limit", 20))
	handler.writeFeed(writer, request, page, err)
}

func (handler *DiscoveryHandler) hot(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, authenticated := identity.PrincipalFromContext(request.Context())
	var page discovery.FeedPage
	var err error
	if authenticated {
		page, err = handler.repository.HotFor(request.Context(), principal.UserID, parseQueryInt(context, "limit", 20))
	} else {
		page, err = handler.repository.Hot(request.Context(), parseQueryInt(context, "limit", 20))
	}
	handler.writeFeed(writer, request, page, err)
}

func (handler *DiscoveryHandler) recommendations(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	page, err := handler.repository.Recommend(request.Context(), principal.UserID, parseQueryInt(context, "limit", 20))
	handler.writeFeed(writer, request, page, err)
}

func (handler *DiscoveryHandler) writeFeed(writer http.ResponseWriter, request *http.Request, page discovery.FeedPage, err error) {
	switch {
	case err == nil:
		httpx.WriteJSON(writer, http.StatusOK, page)
	case errors.Is(err, discovery.ErrInvalidFeedRequest), errors.Is(err, discovery.ErrInvalidFeedCursor):
		httpx.WriteError(writer, request, http.StatusUnprocessableEntity, "invalid_feed_request", err.Error())
	default:
		handler.logger.ErrorContext(request.Context(), "discovery feed failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
