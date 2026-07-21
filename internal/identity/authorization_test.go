package identity_test

import (
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/identity"
)

// TestOwnershipAuthorizationRejectsOtherMemberAndAllowsModerator verifies that owners may edit their own resources, ordinary members may not edit another user's resources, and moderators and admins may.
func TestOwnershipAuthorizationRejectsOtherMemberAndAllowsModerator(t *testing.T) {
	owner := identity.Principal{UserID: "owner", Role: "member"}
	other := identity.Principal{UserID: "other", Role: "member"}
	moderator := identity.Principal{UserID: "moderator", Role: "moderator"}
	admin := identity.Principal{UserID: "admin", Role: "admin"}

	if !owner.CanEdit("owner") {
		t.Fatal("owner cannot edit own resource")
	}
	if other.CanEdit("owner") {
		t.Fatal("non-owner member can edit another user's resource")
	}
	if !moderator.CanEdit("owner") || !admin.CanEdit("owner") {
		t.Fatal("moderator/admin cannot edit another user's resource")
	}
}

// TestAccessTokenRejectsTamperingAndExpiry verifies that a newly issued access token fails verification after modification or after its configured lifetime has elapsed.
func TestAccessTokenRejectsTamperingAndExpiry(t *testing.T) {
	manager := identity.NewTokenManager([]byte("0123456789abcdef0123456789abcdef"), "sea-music", time.Minute)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	token, _, err := manager.Issue(identity.User{ID: "user-id", Role: "member"}, "session-id", now)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if _, err := manager.Verify(token+"tampered", now); err == nil {
		t.Fatal("tampered token was accepted")
	}
	if _, err := manager.Verify(token, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired token was accepted")
	}
}
