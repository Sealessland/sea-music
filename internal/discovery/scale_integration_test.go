package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/discovery"
)

func TestFollowingFeedTraversesHighFollowCardinalityWithDeepCursor(t *testing.T) {
	database := discoveryTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	viewerID, _ := insertDiscoveryUsers(t, ctx, database)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO identity.users (username, email, password_hash)
		SELECT 'bulk_creator_' || value, 'bulk_creator_' || value || '@example.com', 'hash'
		FROM generate_series(1, 250) value
	`); err != nil {
		t.Fatalf("insert bulk creators: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO social.follows (follower_id, followee_id)
		SELECT $1, id FROM identity.users WHERE username LIKE 'bulk_creator_%'
	`, viewerID); err != nil {
		t.Fatalf("insert bulk follows: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO video.videos (creator_id, title, category, state, version, published_at)
		SELECT id, 'bulk feed video', 'general', 'published', 1,
		       now() - (row_number() OVER (ORDER BY id) * interval '1 second')
		FROM identity.users WHERE username LIKE 'bulk_creator_%'
	`); err != nil {
		t.Fatalf("insert bulk feed videos: %v", err)
	}
	repository := discovery.NewPostgresRepository(database)
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := repository.Following(ctx, viewerID, cursor, 17)
		if err != nil {
			t.Fatalf("Following(cursor depth %d): %v", len(seen), err)
		}
		for _, item := range page.Items {
			if seen[item.ID] {
				t.Fatalf("duplicate video %s at cursor depth %d", item.ID, len(seen))
			}
			seen[item.ID] = true
		}
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" {
			t.Fatal("deep page indicated more without cursor")
		}
		cursor = page.NextCursor
	}
	if len(seen) != 250 {
		t.Fatalf("deep cursor returned %d videos, want 250", len(seen))
	}
}
