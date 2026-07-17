package appapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/identity"
	"github.com/sealessland/sea-music/internal/platform/httpx"
)

type Authenticator struct {
	tokens *identity.TokenManager
}

func NewAuthenticator(tokens *identity.TokenManager) *Authenticator {
	return &Authenticator{tokens: tokens}
}

func (auth *Authenticator) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		parts := strings.Split(authorization, " ")
		if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
			httpx.WriteError(writer, request, http.StatusUnauthorized, "authentication_required", "valid bearer token required")
			return
		}
		claims, err := auth.tokens.Verify(parts[1], time.Now())
		if err != nil {
			httpx.WriteError(writer, request, http.StatusUnauthorized, "invalid_access_token", "access token is invalid")
			return
		}
		principal := identity.Principal{UserID: claims.Subject, Role: claims.Role, SessionID: claims.SessionID}
		next.ServeHTTP(writer, request.WithContext(identity.WithPrincipal(request.Context(), principal)))
	})
}

func (auth *Authenticator) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization := request.Header.Get("Authorization")
		if authorization == "" {
			next.ServeHTTP(writer, request)
			return
		}
		parts := strings.Split(authorization, " ")
		if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
			httpx.WriteError(writer, request, http.StatusUnauthorized, "invalid_access_token", "access token is invalid")
			return
		}
		claims, err := auth.tokens.Verify(parts[1], time.Now())
		if err != nil {
			httpx.WriteError(writer, request, http.StatusUnauthorized, "invalid_access_token", "access token is invalid")
			return
		}
		principal := identity.Principal{UserID: claims.Subject, Role: claims.Role, SessionID: claims.SessionID}
		next.ServeHTTP(writer, request.WithContext(identity.WithPrincipal(request.Context(), principal)))
	})
}

// RequireGin is the Gin middleware used by the API router. The HTTP adapter
// methods above remain for compatibility with focused handler tests.
func (auth *Authenticator) RequireGin() gin.HandlerFunc {
	return auth.ginAuthenticate(true)
}

func (auth *Authenticator) OptionalGin() gin.HandlerFunc {
	return auth.ginAuthenticate(false)
}

func (auth *Authenticator) ginAuthenticate(required bool) gin.HandlerFunc {
	return func(context *gin.Context) {
		authorization := context.GetHeader("Authorization")
		if authorization == "" && !required {
			context.Next()
			return
		}
		parts := strings.Split(authorization, " ")
		if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
			httpx.WriteError(context.Writer, context.Request, http.StatusUnauthorized,
				map[bool]string{true: "authentication_required", false: "invalid_access_token"}[required],
				map[bool]string{true: "valid bearer token required", false: "access token is invalid"}[required])
			context.Abort()
			return
		}
		claims, err := auth.tokens.Verify(parts[1], time.Now())
		if err != nil {
			httpx.WriteError(context.Writer, context.Request, http.StatusUnauthorized, "invalid_access_token", "access token is invalid")
			context.Abort()
			return
		}
		principal := identity.Principal{UserID: claims.Subject, Role: claims.Role, SessionID: claims.SessionID}
		context.Request = context.Request.WithContext(identity.WithPrincipal(context.Request.Context(), principal))
		context.Next()
	}
}
