package discovery_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/discovery"
)

func TestAllDiscoveryFeedsFilterBlockedAndWithdrawnCandidates(t *testing.T) {
	database := discoveryTestDatabase(t)
	options, err := redis.ParseURL(os.Getenv("SEA_REDIS_TEST_URL"))
	if err != nil {
		t.Skip("SEA_REDIS_TEST_URL is required")
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush Redis: %v", err)
	}
	viewerID, creatorID := insertDiscoveryUsers(t, ctx, database)
	var videoID string
	if err := database.QueryRowContext(ctx, `
		INSERT INTO video.videos (creator_id, title, category, state, version, published_at)
		VALUES ($1, 'cached candidate', 'music', 'published', 1, now()) RETURNING id::text
	`, creatorID).Scan(&videoID); err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO social.follows (follower_id, followee_id) VALUES ($1, $2)`, viewerID, creatorID); err != nil {
		t.Fatalf("insert follow: %v", err)
	}
	if err := client.ZAdd(ctx, "sea:hot:24h", redis.Z{Score: 100, Member: videoID}).Err(); err != nil {
		t.Fatalf("cache hot candidate: %v", err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO social.blocks (blocker_id, blocked_id) VALUES ($1, $2)`, viewerID, creatorID); err != nil {
		t.Fatalf("insert block: %v", err)
	}
	repository := discovery.NewPostgresRepository(database).WithRanking(client)
	following, err := repository.Following(ctx, viewerID, "", 10)
	if err != nil || len(following.Items) != 0 {
		t.Fatalf("blocked Following() = (%+v, %v)", following, err)
	}
	hot, err := repository.HotFor(ctx, viewerID, 10)
	if err != nil || len(hot.Items) != 0 {
		t.Fatalf("blocked HotFor() = (%+v, %v)", hot, err)
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM social.blocks WHERE blocker_id = $1 AND blocked_id = $2`, viewerID, creatorID); err != nil {
		t.Fatalf("remove block: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE video.videos SET state = 'withdrawn' WHERE id = $1`, videoID); err != nil {
		t.Fatalf("withdraw cached candidate: %v", err)
	}
	hot, err = repository.HotFor(ctx, viewerID, 10)
	if err != nil || len(hot.Items) != 0 {
		t.Fatalf("withdrawn HotFor() = (%+v, %v)", hot, err)
	}
}
