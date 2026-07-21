package social_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/social"
)

// TestDuplicateInteractionEventUpdatesPersistentAndCachedCountOnce verifies that inbox deduplication applies a repeated like event only once to both the persistent projection and Redis cache.
func TestDuplicateInteractionEventUpdatesPersistentAndCachedCountOnce(t *testing.T) {
	database := socialTestDatabase(t)
	redisURL := os.Getenv("SEA_REDIS_TEST_URL")
	if redisURL == "" {
		t.Skip("SEA_REDIS_TEST_URL is required")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse Redis URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush Redis: %v", err)
	}
	userID, _, videoID := insertSocialFixture(t, ctx, database)
	projector := social.NewCounterProjector(client)
	inbox := events.NewInbox(database)
	payload, _ := json.Marshal(map[string]any{"actor_id": userID, "target_id": videoID, "exists": true, "relation": "like"})
	envelope := events.Envelope{
		ID: "01980c55-7c80-7abc-8def-0123456789b0", Type: "social.like.changed", Version: 1,
		AggregateType: "like", AggregateID: videoID, AggregateVersion: 1,
		OccurredAt: time.Now().UTC(), Data: payload,
	}
	handler := func(ctx context.Context, transaction *sql.Tx, envelope events.Envelope) error {
		return projector.Handle(ctx, transaction, social.CounterEvent{ID: envelope.ID, Type: envelope.Type, Data: envelope.Data})
	}
	if processed, err := inbox.Process(ctx, "social-counters", envelope, handler); err != nil || !processed {
		t.Fatalf("first Process() = (%v, %v)", processed, err)
	}
	if processed, err := inbox.Process(ctx, "social-counters", envelope, handler); err != nil || processed {
		t.Fatalf("duplicate Process() = (%v, %v)", processed, err)
	}
	counts, err := projector.Get(ctx, database, videoID)
	if err != nil {
		t.Fatalf("Get(): %v", err)
	}
	if counts.Likes != 1 || counts.Favorites != 0 || counts.Comments != 0 || counts.Danmaku != 0 {
		t.Fatalf("projected counts = %+v", counts)
	}
	cached, err := client.HGet(ctx, "sea:counters:"+videoID, "likes").Int64()
	if err != nil || cached != 1 {
		t.Fatalf("cached likes = (%d, %v)", cached, err)
	}
}
