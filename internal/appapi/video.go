package appapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/httpx"
	"github.com/sealessland/sea-music/internal/video"
)

type VideoHandler struct {
	repository  *video.PostgresRepository
	uploads     *video.UploadService
	publication *video.PublicationService
	auth        *Authenticator
	logger      *slog.Logger
}

func NewVideoHandler(repository *video.PostgresRepository, uploads *video.UploadService, publication *video.PublicationService, auth *Authenticator, logger *slog.Logger) *VideoHandler {
	return &VideoHandler{repository: repository, uploads: uploads, publication: publication, auth: auth, logger: logger}
}

func (handler *VideoHandler) RegisterRoutes(router gin.IRouter) {
	router.POST("/api/v1/videos", handler.auth.Require(), handler.createDraft)
	router.POST("/api/v1/videos/:video_id/uploads", handler.auth.Require(), handler.createUpload)
	router.POST("/api/v1/videos/:video_id/finalize", handler.auth.Require(), handler.finalizeUpload)
	router.POST("/api/v1/videos/:video_id/review", handler.auth.Require(), handler.reviewVideo)
	router.POST("/api/v1/videos/:video_id/withdraw", handler.auth.Require(), handler.withdrawVideo)
	router.GET("/api/v1/videos/:video_id", handler.getPublicVideo)
}

func (handler *VideoHandler) reviewVideo(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	var input struct {
		ExpectedVersion int64  `json:"expected_version"`
		Approved        bool   `json:"approved"`
		Reason          string `json:"reason"`
	}
	if err := bindJSON(context, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := handler.publication.Review(request.Context(), context.Param("video_id"), video.Actor{UserID: principal.UserID, Role: principal.Role}, input.ExpectedVersion, input.Approved, input.Reason)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, map[string]any{"video": result})
}

func (handler *VideoHandler) withdrawVideo(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	var input struct {
		ExpectedVersion int64  `json:"expected_version"`
		Reason          string `json:"reason"`
	}
	if err := bindJSON(context, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	result, err := handler.publication.Withdraw(request.Context(), context.Param("video_id"), video.Actor{UserID: principal.UserID, Role: principal.Role}, input.ExpectedVersion, input.Reason)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, map[string]any{"video": result})
}

func (handler *VideoHandler) getPublicVideo(context *gin.Context) {
	writer, request := context.Writer, context.Request
	result, err := handler.publication.GetPublic(request.Context(), context.Param("video_id"))
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, map[string]any{"video": result})
}

func (handler *VideoHandler) createDraft(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	var input struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := bindJSON(context, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	draft, err := handler.repository.CreateDraft(request.Context(), principal.UserID, input.Title, input.Description)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"video": draft})
}

func (handler *VideoHandler) createUpload(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	var input struct {
		SizeBytes      int64  `json:"size_bytes"`
		ContentType    string `json:"content_type"`
		ChecksumSHA256 string `json:"checksum_sha256"`
	}
	if err := bindJSON(context, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	grant, err := handler.uploads.CreateGrant(request.Context(), video.UploadRequest{
		VideoID: context.Param("video_id"), CreatorID: principal.UserID, SizeBytes: input.SizeBytes,
		ContentType: input.ContentType, ChecksumSHA256: input.ChecksumSHA256,
	})
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"upload": grant})
}

func (handler *VideoHandler) finalizeUpload(context *gin.Context) {
	writer, request := context.Writer, context.Request
	principal, _ := identity.PrincipalFromContext(request.Context())
	result, err := handler.uploads.Finalize(request.Context(), context.Param("video_id"), principal.UserID)
	if err != nil {
		handler.writeError(writer, request, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, result)
}

func (handler *VideoHandler) writeError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, video.ErrInvalidUpload), errors.Is(err, video.ErrInvalidTransition):
		httpx.WriteError(writer, request, http.StatusUnprocessableEntity, "invalid_video_upload", err.Error())
	case errors.Is(err, video.ErrUploadForbidden):
		httpx.WriteError(writer, request, http.StatusForbidden, "forbidden", "video is not editable by this principal")
	case errors.Is(err, video.ErrModerationForbidden):
		httpx.WriteError(writer, request, http.StatusForbidden, "moderation_forbidden", "moderator or admin role required")
	case errors.Is(err, video.ErrVideoNotFound):
		httpx.WriteError(writer, request, http.StatusNotFound, "video_not_found", "video not found")
	case errors.Is(err, video.ErrPublicVideoNotFound):
		httpx.WriteError(writer, request, http.StatusNotFound, "public_video_not_found", "published video not found")
	case errors.Is(err, video.ErrVersionConflict):
		httpx.WriteError(writer, request, http.StatusConflict, "version_conflict", "video version changed")
	default:
		handler.logger.ErrorContext(request.Context(), "video operation failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
