package social_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/social"
)

// TestReconciliationRepairsDeliberateDatabaseAndCacheDrift verifies that reconciliation restores database and Redis counters from authoritative social records and writes a single audit row.
func TestReconciliationRepairsDeliberateDatabaseAndCacheDrift(t *testing.T) {
	database := socialTestDatabase(t)
	options, err := redis.ParseURL(os.Getenv("SEA_REDIS_TEST_URL"))
	if err != nil {
		t.Skip("SEA_REDIS_TEST_URL is required")
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush Redis: %v", err)
	}
	userID, _, videoID := insertSocialFixture(t, ctx, database)
	if _, err := database.ExecContext(ctx, `INSERT INTO social.video_likes (user_id, video_id) VALUES ($1, $2)`, userID, videoID); err != nil {
		t.Fatalf("insert authoritative like: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO social.video_counters (video_id, likes, favorites, comments, danmaku) VALUES ($1, 99, 8, 7, 6)`, videoID); err != nil {
		t.Fatalf("insert drifted snapshot: %v", err)
	}
	if err := client.HSet(ctx, "sea:counters:"+videoID, "likes", 123, "favorites", 123).Err(); err != nil {
		t.Fatalf("insert drifted cache: %v", err)
	}
	reconciler := social.NewCounterReconciler(database, client)
	result, err := reconciler.Reconcile(ctx, videoID)
	if err != nil {
		t.Fatalf("Reconcile(): %v", err)
	}
	if !result.Repaired || result.DriftTotal == 0 || result.After.Likes != 1 || result.After.Favorites != 0 {
		t.Fatalf("reconciliation result = %+v", result)
	}
	var auditRows int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM social.counter_reconciliations WHERE video_id = $1`, videoID).Scan(&auditRows); err != nil {
		t.Fatalf("count reconciliation audits: %v", err)
	}
	cached, err := client.HGet(ctx, "sea:counters:"+videoID, "likes").Int64()
	if auditRows != 1 || err != nil || cached != 1 {
		t.Fatalf("repaired evidence = audits %d cached likes %d error %v", auditRows, cached, err)
	}
}
