package appapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/httpx"
)

type EventsAdminHandler struct {
	replay *events.ReplayService
	auth   *Authenticator
	logger *slog.Logger
}

func NewEventsAdminHandler(replay *events.ReplayService, auth *Authenticator, logger *slog.Logger) *EventsAdminHandler {
	return &EventsAdminHandler{replay: replay, auth: auth, logger: logger}
}

func (handler *EventsAdminHandler) RegisterRoutes(router gin.IRouter) {
	router.POST("/api/v1/admin/dead-letters/:dead_letter_id/replay", handler.auth.RequireGin(), ginHandler(handler.replayDeadLetter))
	router.POST("/api/v1/admin/outbox-events/:event_id/replay", handler.auth.RequireGin(), ginHandler(handler.replayOutboxEvent))
}

func (handler *EventsAdminHandler) replayDeadLetter(writer http.ResponseWriter, request *http.Request) {
	principal, _ := identity.PrincipalFromContext(request.Context())
	err := handler.replay.Replay(request.Context(), request.PathValue("dead_letter_id"), principal.Role)
	switch {
	case err == nil:
		httpx.WriteJSON(writer, http.StatusOK, map[string]string{"status": "replayed"})
	case errors.Is(err, events.ErrReplayForbidden):
		httpx.WriteError(writer, request, http.StatusForbidden, "replay_forbidden", "admin role required")
	case errors.Is(err, events.ErrDeadLetterNotFound):
		httpx.WriteError(writer, request, http.StatusNotFound, "dead_letter_not_found", "quarantined dead letter not found")
	default:
		handler.logger.ErrorContext(request.Context(), "dead letter replay failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (handler *EventsAdminHandler) replayOutboxEvent(writer http.ResponseWriter, request *http.Request) {
	principal, _ := identity.PrincipalFromContext(request.Context())
	err := handler.replay.ReplayOutboxEvent(request.Context(), request.PathValue("event_id"), principal.Role)
	switch {
	case err == nil:
		httpx.WriteJSON(writer, http.StatusOK, map[string]string{"status": "replayed"})
	case errors.Is(err, events.ErrReplayForbidden):
		httpx.WriteError(writer, request, http.StatusForbidden, "replay_forbidden", "admin role required")
	case errors.Is(err, events.ErrOutboxEventNotFound):
		httpx.WriteError(writer, request, http.StatusNotFound, "outbox_event_not_found", "outbox event not found")
	case errors.Is(err, events.ErrOutboxEventNotFailed):
		httpx.WriteError(writer, request, http.StatusConflict, "outbox_event_not_failed", "outbox event is not in failed state")
	default:
		handler.logger.ErrorContext(request.Context(), "outbox event replay failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}
