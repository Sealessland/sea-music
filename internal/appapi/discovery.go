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

// NewDiscoveryHandler creates a discovery HTTP handler that uses the supplied repository, authenticator, and logger.
func NewDiscoveryHandler(repository *discovery.PostgresRepository, auth *Authenticator, logger *slog.Logger) *DiscoveryHandler {
	return &DiscoveryHandler{repository: repository, auth: auth, logger: logger}
}

// RegisterRoutes registers the following, hot, and recommendation feed endpoints with their required or optional authentication middleware.
func (handler *DiscoveryHandler) RegisterRoutes(router gin.IRouter) {
	router.GET("/api/v1/feed/following", handler.auth.Require(), handler.following)
	router.GET("/api/v1/feed/hot", handler.auth.Optional(), handler.hot)
	router.GET("/api/v1/feed/recommendations", handler.auth.Require(), handler.recommendations)
}

// following returns a cursor-paginated feed for the authenticated user's followed accounts, using a default limit of 20.
func (handler *DiscoveryHandler) following(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	page, err := handler.repository.Following(request.Context(), principal.UserID, context.Query("cursor"), parseQueryInt(context, "limit", 20))
	handler.writeFeed(writer, request, page, err)
}

// hot returns a hot feed personalized for an authenticated user or a global hot feed otherwise, using a default limit of 20.
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

// recommendations returns a recommendation feed for the authenticated user, using a default limit of 20.
func (handler *DiscoveryHandler) recommendations(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	page, err := handler.repository.Recommend(request.Context(), principal.UserID, parseQueryInt(context, "limit", 20))
	handler.writeFeed(writer, request, page, err)
}

// writeFeed writes a successful feed as JSON, maps invalid requests or cursors to HTTP 422, and logs all other errors before returning HTTP 500.
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
