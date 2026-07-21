package appapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sealessland/sea-music/internal/appapi"
	"github.com/sealessland/sea-music/internal/identity"
)

// TestOptionalAuthenticationAllowsAnonymousAndAttachesValidPrincipal verifies that optional authentication lets requests without credentials reach the handler without a principal and attaches the issued user's principal for a valid bearer token.
func TestOptionalAuthenticationAllowsAnonymousAndAttachesValidPrincipal(t *testing.T) {
	tokens := identity.NewTokenManager([]byte(strings.Repeat("k", 32)), "test", time.Hour)
	auth := appapi.NewAuthenticator(tokens)
	handler := gin.New()
	handler.GET("/", auth.Optional(), func(context *gin.Context) {
		principal, ok := identity.PrincipalFromContext(context.Request.Context())
		if !ok {
			context.Status(http.StatusNoContent)
			return
		}
		context.String(http.StatusOK, principal.UserID)
	})

	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/", nil))
	if anonymous.Code != http.StatusNoContent {
		t.Fatalf("anonymous status = %d, want 204", anonymous.Code)
	}

	token, _, err := tokens.Issue(identity.User{ID: "user-1", Role: "member"}, "session-1", time.Now())
	if err != nil {
		t.Fatalf("Issue(): %v", err)
	}
	authenticatedRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	authenticatedRequest.Header.Set("Authorization", "Bearer "+token)
	authenticated := httptest.NewRecorder()
	handler.ServeHTTP(authenticated, authenticatedRequest)
	if authenticated.Code != http.StatusOK || authenticated.Body.String() != "user-1" {
		t.Fatalf("authenticated response = (%d, %q)", authenticated.Code, authenticated.Body.String())
	}
}

// TestOptionalAuthenticationRejectsInvalidBearerToken verifies that optional authentication returns HTTP 401 for a malformed bearer token and does not invoke the next handler.
func TestOptionalAuthenticationRejectsInvalidBearerToken(t *testing.T) {
	auth := appapi.NewAuthenticator(identity.NewTokenManager([]byte(strings.Repeat("k", 32)), "test", time.Hour))
	handler := gin.New()
	handler.GET("/", auth.Optional(), func(*gin.Context) {
		t.Fatal("next handler called for invalid token")
	})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer broken")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
