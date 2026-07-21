package appapi

import (
	"errors"
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

// NewIdentityHandler constructs an identity HTTP handler that uses the token manager for bearer authentication and retains the supplied rate-limit policies and logger for route handling.
func NewIdentityHandler(service *identity.Service, tokens *identity.TokenManager, limits *RateLimitMiddleware, writePolicy, readPolicy ratelimit.Policy, logger *slog.Logger) *IdentityHandler {
	return &IdentityHandler{service: service, auth: NewAuthenticator(tokens), limits: limits, writePolicy: writePolicy, readPolicy: readPolicy, logger: logger}
}

// RegisterRoutes mounts the user registration, login, token refresh, and authenticated current-user endpoints with their respective write or read rate limits.
func (h *IdentityHandler) RegisterRoutes(router gin.IRouter) {
	router.POST("/api/v1/users", h.limits.GinWrap("identity_write", h.writePolicy, false), h.register)
	router.POST("/api/v1/sessions", h.limits.GinWrap("identity_write", h.writePolicy, false), h.login)
	router.POST("/api/v1/sessions/refresh", h.limits.GinWrap("identity_write", h.writePolicy, false), h.refresh)
	router.GET("/api/v1/me", h.auth.Require(), h.limits.GinWrap("identity_read", h.readPolicy, true), h.me)
}

// register validates a registration request, creates the user, and returns 201, mapping validation and identity conflicts to 400 and 409 while logging unexpected failures.
func (h *IdentityHandler) register(context *gin.Context) {
	writer, request := context.Writer, context.Request
	var input identity.RegisterInput
	if err := bindJSON(context, &input); err != nil {
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

// me returns the authenticated principal's current user, treating a missing principal or user as an authentication failure and logging unexpected lookup errors.
func (h *IdentityHandler) me(context *gin.Context) {
	writer, request := context.Writer, context.Request
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

// login validates credentials from the request and returns a token pair, mapping invalid credentials to 401 and logging unexpected failures.
func (h *IdentityHandler) login(context *gin.Context) {
	writer, request := context.Writer, context.Request
	var input identity.LoginInput
	if err := bindJSON(context, &input); err != nil {
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

// refresh exchanges a refresh token for a new token pair, mapping invalid or replayed tokens to the same 401 response and logging unexpected failures.
func (h *IdentityHandler) refresh(context *gin.Context) {
	writer, request := context.Writer, context.Request
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := bindJSON(context, &input); err != nil {
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
