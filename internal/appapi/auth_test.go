package appapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/appapi"
	"github.com/sealessland/sea-music/internal/identity"
)

func TestOptionalAuthenticationAllowsAnonymousAndAttachesValidPrincipal(t *testing.T) {
	tokens := identity.NewTokenManager([]byte(strings.Repeat("k", 32)), "test", time.Hour)
	auth := appapi.NewAuthenticator(tokens)
	handler := auth.Optional(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		principal, ok := identity.PrincipalFromContext(request.Context())
		if !ok {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = writer.Write([]byte(principal.UserID))
	}))

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

func TestOptionalAuthenticationRejectsInvalidBearerToken(t *testing.T) {
	auth := appapi.NewAuthenticator(identity.NewTokenManager([]byte(strings.Repeat("k", 32)), "test", time.Hour))
	handler := auth.Optional(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next handler called for invalid token")
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer broken")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
