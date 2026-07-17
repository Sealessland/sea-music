package discovery_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sealessland/sea-music/internal/discovery"
)

func TestHotRankingDeduplicatesEventsAndFallsBackToPersistedSnapshot(t *testing.T) {
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
	_, creatorID := insertDiscoveryUsers(t, ctx, database)
	var videoID string
	if err := database.QueryRowContext(ctx, `
		INSERT INTO video.videos (creator_id, title, state, version, published_at)
		VALUES ($1, 'hot video', 'published', 1, now()) RETURNING id::text
	`, creatorID).Scan(&videoID); err != nil {
		t.Fatalf("insert hot video: %v", err)
	}
	projector := discovery.NewHotProjector(database, client, 24*time.Hour)
	payload, _ := json.Marshal(map[string]any{"target_id": videoID, "exists": true})
	event := discovery.EngagementEvent{ID: "01980c55-7c80-7abc-8def-0123456789b1", Type: "social.like.changed", OccurredAt: time.Now().UTC(), Data: payload}
	for range 2 {
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx(): %v", err)
		}
		if err := projector.Handle(ctx, transaction, event); err != nil {
			_ = transaction.Rollback()
			t.Fatalf("Handle(): %v", err)
		}
		if err := transaction.Commit(); err != nil {
			t.Fatalf("Commit(): %v", err)
		}
	}
	var deduped int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM discovery.engagement_events WHERE event_id = $1`, event.ID).Scan(&deduped); err != nil || deduped != 1 {
		t.Fatalf("deduped events = (%d, %v)", deduped, err)
	}
	repository := discovery.NewPostgresRepository(database).WithRanking(client)
	live, err := repository.Hot(ctx, 10)
	if err != nil || live.Degraded || len(live.Items) != 1 || live.Items[0].ID != videoID {
		t.Fatalf("live Hot() = (%+v, %v)", live, err)
	}
	brokenRedis := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond, ReadTimeout: 20 * time.Millisecond})
	defer brokenRedis.Close()
	fallback := discovery.NewPostgresRepository(database).WithRanking(brokenRedis)
	degraded, err := fallback.Hot(ctx, 10)
	if err != nil || !degraded.Degraded || len(degraded.Items) != 1 || degraded.Items[0].ID != videoID {
		t.Fatalf("fallback Hot() = (%+v, %v)", degraded, err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO video.videos (creator_id, title, category, state, version, published_at)
		VALUES ($1, 'recent one', 'music', 'published', 1, now() - interval '1 minute'),
		       ($1, 'recent two', 'knowledge', 'published', 1, now() - interval '2 minutes')
	`, creatorID); err != nil {
		t.Fatalf("insert recent fallback videos: %v", err)
	}
	expanded, err := repository.Hot(ctx, 3)
	if err != nil || len(expanded.Items) != 3 || expanded.Items[0].ID != videoID {
		t.Fatalf("sparse ranking Hot() = (%+v, %v), want ranked item plus two recent items", expanded, err)
	}
	if expanded.Items[1].ReasonCode != "recent_fallback" || expanded.Items[2].ReasonCode != "recent_fallback" {
		t.Fatalf("recent fallback reasons = (%q, %q)", expanded.Items[1].ReasonCode, expanded.Items[2].ReasonCode)
	}
}

func TestHotRankingKeepsRankOrderWhileFilteringInvisibleCandidates(t *testing.T) {
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
	var blockedCreatorID string
	if err := database.QueryRowContext(ctx, `INSERT INTO identity.users (username, email, password_hash) VALUES ('blocked_creator', 'blocked@example.com', 'hash') RETURNING id::text`).Scan(&blockedCreatorID); err != nil {
		t.Fatalf("insert blocked creator: %v", err)
	}
	insertVideo := func(ownerID, title, state string) string {
		t.Helper()
		var id string
		if err := database.QueryRowContext(ctx, `
			INSERT INTO video.videos (creator_id, title, state, version, published_at)
			VALUES ($1, $2, $3, 1, now()) RETURNING id::text
		`, ownerID, title, state).Scan(&id); err != nil {
			t.Fatalf("insert %s candidate: %v", state, err)
		}
		return id
	}
	topID := insertVideo(creatorID, "rank 1 published", "published")
	draftID := insertVideo(creatorID, "rank 2 draft", "draft")
	blockedID := insertVideo(blockedCreatorID, "rank 3 blocked", "published")
	thirdID := insertVideo(creatorID, "rank 4 published", "published")
	withdrawnID := insertVideo(creatorID, "rank 5 withdrawn", "withdrawn")
	for index, member := range []string{topID, draftID, blockedID, thirdID, withdrawnID} {
		if err := client.ZAdd(ctx, "sea:hot:24h", redis.Z{Score: float64(100 - index), Member: member}).Err(); err != nil {
			t.Fatalf("cache hot candidate: %v", err)
		}
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO social.blocks (blocker_id, blocked_id) VALUES ($1, $2)`, viewerID, blockedCreatorID); err != nil {
		t.Fatalf("insert block: %v", err)
	}
	repository := discovery.NewPostgresRepository(database).WithRanking(client)
	page, err := repository.HotFor(ctx, viewerID, 2)
	if err != nil {
		t.Fatalf("HotFor(): %v", err)
	}
	if page.Degraded || len(page.Items) != 2 || page.Items[0].ID != topID || page.Items[1].ID != thirdID {
		t.Fatalf("viewer HotFor() = (%+v, %v), want ranked [%s %s]", page, err, topID, thirdID)
	}
	for _, item := range page.Items {
		if item.ReasonCode != "hot_window" {
			t.Fatalf("ReasonCode = %q, want hot_window", item.ReasonCode)
		}
	}
	anonymous, err := repository.Hot(ctx, 3)
	if err != nil {
		t.Fatalf("anonymous Hot(): %v", err)
	}
	if len(anonymous.Items) != 3 || anonymous.Items[0].ID != topID || anonymous.Items[1].ID != blockedID || anonymous.Items[2].ID != thirdID {
		t.Fatalf("anonymous Hot() = %+v, want ranked [%s %s %s]", anonymous.Items, topID, blockedID, thirdID)
	}
}
