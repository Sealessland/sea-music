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

// NewAuthenticator creates an Authenticator that uses tokens to verify bearer access tokens.
func NewAuthenticator(tokens *identity.TokenManager) *Authenticator {
	return &Authenticator{tokens: tokens}
}

// Require returns Gin middleware that rejects missing, malformed, or unverifiable bearer tokens with HTTP 401 and otherwise adds the verified principal to the request context.
func (auth *Authenticator) Require() gin.HandlerFunc {
	return auth.ginAuthenticate(true)
}

// Optional returns Gin middleware that permits requests without an Authorization header, but rejects malformed or unverifiable bearer tokens with HTTP 401 and adds the principal from a valid token to the request context.
func (auth *Authenticator) Optional() gin.HandlerFunc {
	return auth.ginAuthenticate(false)
}

// ginAuthenticate returns Gin middleware that verifies bearer tokens, adds valid principals to request contexts, and aborts invalid requests with HTTP 401; when required is false, a missing Authorization header is allowed.
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
