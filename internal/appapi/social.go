package appapi

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/httpx"
	"github.com/sealessland/sea-music/internal/social"
)

type SocialHandler struct {
	repository *social.PostgresRepository
	auth       *Authenticator
	logger     *slog.Logger
}

func NewSocialHandler(repository *social.PostgresRepository, auth *Authenticator, logger *slog.Logger) *SocialHandler {
	return &SocialHandler{repository: repository, auth: auth, logger: logger}
}

func (handler *SocialHandler) RegisterRoutes(router gin.IRouter) {
	router.PUT("/api/v1/videos/:video_id/like", handler.auth.Require(), handler.setLike(true))
	router.DELETE("/api/v1/videos/:video_id/like", handler.auth.Require(), handler.setLike(false))
	router.PUT("/api/v1/videos/:video_id/favorite", handler.auth.Require(), handler.setFavorite(true))
	router.DELETE("/api/v1/videos/:video_id/favorite", handler.auth.Require(), handler.setFavorite(false))
	router.PUT("/api/v1/users/:user_id/follow", handler.auth.Require(), handler.setFollow(true))
	router.DELETE("/api/v1/users/:user_id/follow", handler.auth.Require(), handler.setFollow(false))
	router.POST("/api/v1/videos/:video_id/comments", handler.auth.Require(), handler.createComment)
	router.GET("/api/v1/videos/:video_id/comments", handler.listComments)
	router.DELETE("/api/v1/comments/:comment_id", handler.auth.Require(), handler.deleteComment)
	router.POST("/api/v1/videos/:video_id/danmaku", handler.auth.Require(), handler.createDanmaku)
	router.GET("/api/v1/videos/:video_id/danmaku", handler.listDanmaku)
}

func (handler *SocialHandler) createComment(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	var input struct {
		ParentID string `json:"parent_id"`
		Body     string `json:"body"`
	}
	if err := bindJSON(context, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	comment, err := handler.repository.CreateComment(request.Context(), principal.UserID, context.Param("video_id"), input.ParentID, input.Body)
	if err != nil {
		handler.writeSocialError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"comment": comment})
}

func (handler *SocialHandler) listComments(context *gin.Context) {
	writer, request := context.Writer, context.Request
	limit := parseQueryInt(context, "limit", 20)
	page, err := handler.repository.ListComments(request.Context(), context.Param("video_id"), context.Query("cursor"), limit)
	if err != nil {
		handler.writeSocialError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, page)
}

func (handler *SocialHandler) deleteComment(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	err := handler.repository.DeleteComment(request.Context(), context.Param("comment_id"), social.Actor{UserID: principal.UserID, Role: principal.Role})
	if err != nil {
		handler.writeSocialError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *SocialHandler) createDanmaku(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	var input struct {
		PositionMS int    `json:"position_ms"`
		Body       string `json:"body"`
	}
	if err := bindJSON(context, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	message, err := handler.repository.CreateDanmaku(request.Context(), principal.UserID, context.Param("video_id"), input.PositionMS, input.Body)
	if err != nil {
		handler.writeSocialError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"danmaku": message})
}

func (handler *SocialHandler) listDanmaku(context *gin.Context) {
	writer, request := context.Writer, context.Request
	startMS := parseQueryInt(context, "start_ms", 0)
	endMS := parseQueryInt(context, "end_ms", startMS+300_000)
	limit := parseQueryInt(context, "limit", 100)
	page, err := handler.repository.ListDanmaku(request.Context(), context.Param("video_id"), startMS, endMS, context.Query("cursor"), limit)
	if err != nil {
		handler.writeSocialError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, page)
}

func parseQueryInt(context *gin.Context, key string, fallback int) int {
	raw := context.Query(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}

func (handler *SocialHandler) writeSocialError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, social.ErrCommentNotFound):
		httpx.WriteError(writer, request, http.StatusNotFound, "comment_not_found", "comment not found")
	case errors.Is(err, social.ErrCommentForbidden):
		httpx.WriteError(writer, request, http.StatusForbidden, "comment_forbidden", "comment cannot be deleted by this principal")
	case errors.Is(err, social.ErrDanmakuRateLimited):
		httpx.WriteError(writer, request, http.StatusTooManyRequests, "danmaku_rate_limited", "danmaku rate limit exceeded")
	case errors.Is(err, social.ErrInvalidComment), errors.Is(err, social.ErrInvalidCommentParent),
		errors.Is(err, social.ErrInvalidDanmaku), errors.Is(err, social.ErrInvalidCursor):
		httpx.WriteError(writer, request, http.StatusUnprocessableEntity, "invalid_social_content", err.Error())
	default:
		handler.logger.ErrorContext(request.Context(), "social content failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (handler *SocialHandler) setLike(enabled bool) gin.HandlerFunc {
	return func(context *gin.Context) {
		writer, request := context.Writer, context.Request
		principal, _ := identity.PrincipalFromContext(request.Context())
		result, err := handler.repository.SetLike(request.Context(), principal.UserID, context.Param("video_id"), enabled)
		handler.writeResult(writer, request, result, err)
	}
}

func (handler *SocialHandler) setFavorite(enabled bool) gin.HandlerFunc {
	return func(context *gin.Context) {
		writer, request := context.Writer, context.Request
		principal, _ := identity.PrincipalFromContext(request.Context())
		result, err := handler.repository.SetFavorite(request.Context(), principal.UserID, context.Param("video_id"), enabled)
		handler.writeResult(writer, request, result, err)
	}
}

func (handler *SocialHandler) setFollow(enabled bool) gin.HandlerFunc {
	return func(context *gin.Context) {
		writer, request := context.Writer, context.Request
		principal, _ := identity.PrincipalFromContext(request.Context())
		result, err := handler.repository.SetFollow(request.Context(), principal.UserID, context.Param("user_id"), enabled)
		handler.writeResult(writer, request, result, err)
	}
}

func (handler *SocialHandler) writeResult(writer http.ResponseWriter, request *http.Request, result social.RelationResult, err error) {
	if err == nil {
		httpx.WriteJSON(writer, http.StatusOK, result)
		return
	}
	handler.logger.ErrorContext(request.Context(), "social relation failed", "request_id", httpx.RequestID(request.Context()), "error", err)
	httpx.WriteError(writer, request, http.StatusUnprocessableEntity, "invalid_social_relation", "social relation could not be changed")
}
