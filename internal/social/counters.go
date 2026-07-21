package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

type CounterEvent struct {
	ID   string
	Type string
	Data json.RawMessage
}

type VideoCounters struct {
	VideoID   string `json:"video_id"`
	Likes     int64  `json:"likes"`
	Favorites int64  `json:"favorites"`
	Comments  int64  `json:"comments"`
	Danmaku   int64  `json:"danmaku"`
}

type CounterProjector struct {
	redis redis.UniversalClient
}

// NewCounterProjector creates a projector that persists counter updates and optionally refreshes their Redis cache.
func NewCounterProjector(client redis.UniversalClient) *CounterProjector {
	return &CounterProjector{redis: client}
}

// Handle applies a supported social event to nonnegative video counters within the required transaction and best-effort refreshes Redis; unsupported event types are ignored.
func (projector *CounterProjector) Handle(ctx context.Context, transaction *sql.Tx, event CounterEvent) error {
	if transaction == nil {
		return errors.New("counter projection transaction is required")
	}
	var data struct {
		TargetID string `json:"target_id"`
		VideoID  string `json:"video_id"`
		Exists   bool   `json:"exists"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return fmt.Errorf("decode counter event: %w", err)
	}
	videoID := data.VideoID
	if videoID == "" {
		videoID = data.TargetID
	}
	var column string
	var delta int64 = 1
	switch event.Type {
	case "social.like.changed":
		column = "likes"
		if !data.Exists {
			delta = -1
		}
	case "social.favorite.changed":
		column = "favorites"
		if !data.Exists {
			delta = -1
		}
	case "social.comment.created":
		column = "comments"
	case "social.comment.deleted":
		column = "comments"
		delta = -1
	case "social.danmaku.created":
		column = "danmaku"
	default:
		return nil
	}
	if videoID == "" {
		return errors.New("counter event is missing video id")
	}
	query := fmt.Sprintf(`
		INSERT INTO social.video_counters (video_id, %s)
		VALUES ($1, GREATEST($2, 0))
		ON CONFLICT (video_id) DO UPDATE
		SET %s = GREATEST(social.video_counters.%s + $2, 0), updated_at = now()
		RETURNING video_id::text, likes, favorites, comments, danmaku
	`, column, column, column)
	var counts VideoCounters
	if err := transaction.QueryRowContext(ctx, query, videoID, delta).Scan(
		&counts.VideoID, &counts.Likes, &counts.Favorites, &counts.Comments, &counts.Danmaku,
	); err != nil {
		return fmt.Errorf("project interaction counter: %w", err)
	}
	if projector.redis != nil {
		_ = projector.redis.HSet(ctx, "sea:counters:"+videoID, map[string]any{
			"likes": counts.Likes, "favorites": counts.Favorites,
			"comments": counts.Comments, "danmaku": counts.Danmaku,
		}).Err()
	}
	return nil
}

// Get returns the persisted counters for a video, or zero counters with the requested video ID when no row exists.
func (projector *CounterProjector) Get(ctx context.Context, database *sql.DB, videoID string) (VideoCounters, error) {
	var counts VideoCounters
	err := database.QueryRowContext(ctx, `
		SELECT video_id::text, likes, favorites, comments, danmaku
		FROM social.video_counters WHERE video_id = $1
	`, videoID).Scan(&counts.VideoID, &counts.Likes, &counts.Favorites, &counts.Comments, &counts.Danmaku)
	if errors.Is(err, sql.ErrNoRows) {
		return VideoCounters{VideoID: videoID}, nil
	}
	if err != nil {
		return VideoCounters{}, fmt.Errorf("get video counters: %w", err)
	}
	return counts, nil
}
