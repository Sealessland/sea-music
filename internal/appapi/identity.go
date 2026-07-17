package appapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/httpx"
	"github.com/sealessland/sea-music/internal/platform/ratelimit"
)

type IdentityHandler struct {
	service     *identity.Service
	auth        *Authenticator
	limits      *RateLimitMiddleware
	writePolicy ratelimit.Policy
	readPolicy  ratelimit.Policy
	logger      *slog.Logger
}

func NewIdentityHandler(service *identity.Service, tokens *identity.TokenManager, limits *RateLimitMiddleware, writePolicy, readPolicy ratelimit.Policy, logger *slog.Logger) *IdentityHandler {
	return &IdentityHandler{service: service, auth: NewAuthenticator(tokens), limits: limits, writePolicy: writePolicy, readPolicy: readPolicy, logger: logger}
}

func (h *IdentityHandler) RegisterRoutes(router gin.IRouter) {
	router.POST("/api/v1/users", h.limits.GinWrap("identity_write", h.writePolicy, false), ginHandler(h.register))
	router.POST("/api/v1/sessions", h.limits.GinWrap("identity_write", h.writePolicy, false), ginHandler(h.login))
	router.POST("/api/v1/sessions/refresh", h.limits.GinWrap("identity_write", h.writePolicy, false), ginHandler(h.refresh))
	router.GET("/api/v1/me", h.auth.RequireGin(), h.limits.GinWrap("identity_read", h.readPolicy, true), ginHandler(h.me))
}

func (h *IdentityHandler) register(writer http.ResponseWriter, request *http.Request) {
	var input identity.RegisterInput
	if err := decodeJSON(writer, request, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, err := h.service.Register(request.Context(), input)
	switch {
	case err == nil:
		httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"user": user})
	case errors.Is(err, identity.ErrInvalidRegistration):
		httpx.WriteError(writer, request, http.StatusBadRequest, "validation_failed", err.Error())
	case errors.Is(err, identity.ErrIdentityConflict):
		httpx.WriteError(writer, request, http.StatusConflict, "identity_conflict", "username or email already exists")
	default:
		h.logger.ErrorContext(request.Context(), "register user failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (h *IdentityHandler) me(writer http.ResponseWriter, request *http.Request) {
	principal, ok := identity.PrincipalFromContext(request.Context())
	if !ok {
		httpx.WriteError(writer, request, http.StatusUnauthorized, "authentication_required", "valid bearer token required")
		return
	}
	user, err := h.service.CurrentUser(request.Context(), principal.UserID)
	switch {
	case err == nil:
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"user": user})
	case errors.Is(err, identity.ErrIdentityNotFound):
		httpx.WriteError(writer, request, http.StatusUnauthorized, "invalid_access_token", "access token is invalid")
	default:
		h.logger.ErrorContext(request.Context(), "current user lookup failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (h *IdentityHandler) login(writer http.ResponseWriter, request *http.Request) {
	var input identity.LoginInput
	if err := decodeJSON(writer, request, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	pair, err := h.service.Login(request.Context(), input)
	switch {
	case err == nil:
		httpx.WriteJSON(writer, http.StatusOK, pair)
	case errors.Is(err, identity.ErrInvalidCredentials):
		httpx.WriteError(writer, request, http.StatusUnauthorized, "invalid_credentials", "identity or password is invalid")
	default:
		h.logger.ErrorContext(request.Context(), "login failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func (h *IdentityHandler) refresh(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		httpx.WriteError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	pair, err := h.service.Refresh(request.Context(), input.RefreshToken)
	switch {
	case err == nil:
		httpx.WriteJSON(writer, http.StatusOK, pair)
	case errors.Is(err, identity.ErrInvalidRefresh), errors.Is(err, identity.ErrRefreshReplay):
		httpx.WriteError(writer, request, http.StatusUnauthorized, "invalid_refresh_token", "refresh token is invalid")
	default:
		h.logger.ErrorContext(request.Context(), "refresh failed", "request_id", httpx.RequestID(request.Context()), "error", err)
		httpx.WriteError(writer, request, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, 1<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request body must be valid JSON")
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("extra JSON value")
	}
	return err
}
