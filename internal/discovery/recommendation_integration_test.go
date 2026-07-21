package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/discovery"
)

// TestRecommendationUsesExplicitSignalsAndColdStartDiversity verifies that recommendations initially return three category-diverse recent videos with cold-start reasons, then prioritize a followed creator after the viewer follows and likes that creator's video.
func TestRecommendationUsesExplicitSignalsAndColdStartDiversity(t *testing.T) {
	database := discoveryTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	viewerID, creatorID := insertDiscoveryUsers(t, ctx, database)
	var followedVideoID string
	for index, category := range []string{"music", "games", "technology", "music"} {
		var id string
		if err := database.QueryRowContext(ctx, `
			INSERT INTO video.videos (creator_id, title, category, state, version, published_at)
			VALUES ($1, $2, $3, 'published', 1, $4) RETURNING id::text
		`, creatorID, category+" video", category, time.Now().UTC().Add(-time.Duration(index)*time.Minute)).Scan(&id); err != nil {
			t.Fatalf("insert recommendation video: %v", err)
		}
		if index == 0 {
			followedVideoID = id
		}
	}
	repository := discovery.NewPostgresRepository(database)
	cold, err := repository.Recommend(ctx, viewerID, 3)
	if err != nil || len(cold.Items) != 3 {
		t.Fatalf("cold Recommend() = (%+v, %v)", cold, err)
	}
	categories := map[string]bool{}
	for _, item := range cold.Items {
		categories[item.Category] = true
		if item.ReasonCode != "cold_start_recent" {
			t.Fatalf("cold reason = %q", item.ReasonCode)
		}
	}
	if len(categories) < 3 {
		t.Fatalf("cold-start categories = %v, want diversity", categories)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO social.follows (follower_id, followee_id) VALUES ($1, $2)`, viewerID, creatorID); err != nil {
		t.Fatalf("insert recommendation follow: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO social.video_likes (user_id, video_id) VALUES ($1, $2)`, viewerID, followedVideoID); err != nil {
		t.Fatalf("insert category affinity like: %v", err)
	}
	personalized, err := repository.Recommend(ctx, viewerID, 3)
	if err != nil || len(personalized.Items) == 0 || personalized.Items[0].ReasonCode != "followed_creator" {
		t.Fatalf("personalized Recommend() = (%+v, %v)", personalized, err)
	}
}
