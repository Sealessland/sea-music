package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const hotRankingKey = "sea:hot:24h"

type EngagementEvent struct {
	ID         string
	Type       string
	OccurredAt time.Time
	Data       json.RawMessage
}

type HotProjector struct {
	database *sql.DB
	redis    redis.UniversalClient
	window   time.Duration
}

func NewHotProjector(database *sql.DB, client redis.UniversalClient, window time.Duration) *HotProjector {
	return &HotProjector{database: database, redis: client, window: window}
}

func (projector *HotProjector) Handle(ctx context.Context, transaction *sql.Tx, event EngagementEvent) error {
	if transaction == nil || event.ID == "" || event.OccurredAt.IsZero() {
		return errors.New("invalid engagement event")
	}
	var data struct {
		TargetID string `json:"target_id"`
		VideoID  string `json:"video_id"`
		Exists   bool   `json:"exists"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return fmt.Errorf("decode engagement event: %w", err)
	}
	videoID := data.VideoID
	if videoID == "" {
		videoID = data.TargetID
	}
	weight := engagementWeight(event.Type, data.Exists)
	if videoID == "" || weight == 0 || event.OccurredAt.Before(time.Now().Add(-projector.window)) {
		return nil
	}
	result, err := transaction.ExecContext(ctx, `
		INSERT INTO discovery.engagement_events (event_id, video_id, event_type, weight, occurred_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (event_id) DO NOTHING
	`, event.ID, videoID, event.Type, weight, event.OccurredAt)
	if err != nil {
		return fmt.Errorf("deduplicate engagement event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read engagement dedupe result: %w", err)
	}
	if rows == 0 {
		return nil
	}
	var score float64
	halfLifeSeconds := projector.window.Seconds() / 2
	if err := transaction.QueryRowContext(ctx, `
		INSERT INTO discovery.hot_snapshots (video_id, score, calculated_at)
		VALUES ($1, GREATEST($2, 0), $3)
		ON CONFLICT (video_id) DO UPDATE
		SET score = GREATEST(
			discovery.hot_snapshots.score * exp(-EXTRACT(EPOCH FROM ($3 - discovery.hot_snapshots.calculated_at)) / $4) + $2,
			0
		), calculated_at = $3
		RETURNING score
	`, videoID, weight, event.OccurredAt, halfLifeSeconds).Scan(&score); err != nil {
		return fmt.Errorf("update hot snapshot: %w", err)
	}
	if projector.redis != nil {
		_ = projector.redis.ZAdd(ctx, hotRankingKey, redis.Z{Score: score, Member: videoID}).Err()
		_ = projector.redis.Expire(ctx, hotRankingKey, projector.window).Err()
	}
	return nil
}

func engagementWeight(eventType string, exists bool) float64 {
	sign := 1.0
	if !exists {
		sign = -1
	}
	switch eventType {
	case "social.like.changed":
		return 3 * sign
	case "social.favorite.changed":
		return 5 * sign
	case "social.comment.created":
		return 2
	case "social.comment.deleted":
		return -2
	case "social.danmaku.created":
		return 0.25
	default:
		return 0
	}
}

func (repository *PostgresRepository) Hot(ctx context.Context, limit int) (FeedPage, error) {
	return repository.hotFor(ctx, "", limit)
}

func (repository *PostgresRepository) HotFor(ctx context.Context, viewerID string, limit int) (FeedPage, error) {
	return repository.hotFor(ctx, viewerID, limit)
}

func (repository *PostgresRepository) hotFor(ctx context.Context, viewerID string, limit int) (FeedPage, error) {
	if limit <= 0 || limit > 100 {
		return FeedPage{}, ErrInvalidFeedRequest
	}
	if repository.ranking != nil {
		candidates, err := repository.ranking.ZRevRangeWithScores(ctx, hotRankingKey, 0, int64(limit*3-1)).Result()
		if err == nil {
			items, err := repository.visibleCandidates(ctx, viewerID, rankedIDs(candidates), limit, "hot_window")
			if err != nil {
				return FeedPage{}, err
			}
			items, err = repository.topUpRecent(ctx, viewerID, items, limit)
			return FeedPage{Items: items}, err
		}
	}
	rows, err := repository.database.QueryContext(ctx, `
		SELECT video_id::text FROM discovery.hot_snapshots
		ORDER BY score DESC, video_id LIMIT $1
	`, limit*3)
	if err != nil {
		return FeedPage{}, fmt.Errorf("query hot snapshot fallback: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return FeedPage{}, err
		}
		ids = append(ids, id)
	}
	items, err := repository.visibleCandidates(ctx, viewerID, ids, limit, "hot_snapshot_fallback")
	if err != nil {
		return FeedPage{}, err
	}
	items, err = repository.topUpRecent(ctx, viewerID, items, limit)
	return FeedPage{Items: items, Degraded: true}, err
}

func (repository *PostgresRepository) topUpRecent(ctx context.Context, viewerID string, items []FeedItem, limit int) ([]FeedItem, error) {
	if len(items) >= limit {
		return items, nil
	}
	rows, err := repository.database.QueryContext(ctx, `
		SELECT v.id::text, v.creator_id::text, v.title, v.description, v.category, v.published_at
		FROM video.videos v
		WHERE v.state = 'published'
		  AND (
			NULLIF($1, '')::uuid IS NULL OR NOT EXISTS (
				SELECT 1 FROM social.blocks b
				WHERE (b.blocker_id = NULLIF($1, '')::uuid AND b.blocked_id = v.creator_id)
				   OR (b.blocker_id = v.creator_id AND b.blocked_id = NULLIF($1, '')::uuid)
			)
		  )
		ORDER BY v.published_at DESC, v.id DESC
		LIMIT $2
	`, viewerID, limit*3)
	if err != nil {
		return nil, fmt.Errorf("query recent hot fallback: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		seen[item.ID] = true
	}
	for rows.Next() && len(items) < limit {
		var item FeedItem
		if err := rows.Scan(&item.ID, &item.CreatorID, &item.Title, &item.Description, &item.Category, &item.PublishedAt); err != nil {
			return nil, fmt.Errorf("scan recent hot fallback: %w", err)
		}
		if seen[item.ID] {
			continue
		}
		item.ReasonCode = "recent_fallback"
		items = append(items, item)
		seen[item.ID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent hot fallback: %w", err)
	}
	return items, nil
}

func rankedIDs(candidates []redis.Z) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		switch value := candidate.Member.(type) {
		case string:
			ids = append(ids, value)
		case []byte:
			ids = append(ids, string(value))
		}
	}
	return ids
}

func (repository *PostgresRepository) visibleCandidates(ctx context.Context, viewerID string, ids []string, limit int, reason string) ([]FeedItem, error) {
	items := make([]FeedItem, 0, min(limit, len(ids)))
	if len(ids) == 0 {
		return items, nil
	}
	rows, err := repository.database.QueryContext(ctx, `
		SELECT v.id::text, v.creator_id::text, v.title, v.description, v.category, v.published_at
		FROM video.videos v
		WHERE v.id = ANY($1::text[]::uuid[]) AND v.state = 'published'
		  AND (
			NULLIF($2, '')::uuid IS NULL OR NOT EXISTS (
				SELECT 1 FROM social.blocks b
				WHERE (b.blocker_id = NULLIF($2, '')::uuid AND b.blocked_id = v.creator_id)
				   OR (b.blocker_id = v.creator_id AND b.blocked_id = NULLIF($2, '')::uuid)
			)
		  )
	`, ids, viewerID)
	if err != nil {
		return nil, fmt.Errorf("filter ranked candidates: %w", err)
	}
	defer rows.Close()
	visible := make(map[string]FeedItem, len(ids))
	for rows.Next() {
		var item FeedItem
		if err := rows.Scan(&item.ID, &item.CreatorID, &item.Title, &item.Description, &item.Category, &item.PublishedAt); err != nil {
			return nil, fmt.Errorf("filter ranked candidates: %w", err)
		}
		visible[item.ID] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filter ranked candidates: %w", err)
	}
	for _, id := range ids {
		item, ok := visible[id]
		if !ok {
			continue
		}
		item.ReasonCode = reason
		items = append(items, item)
		if len(items) == limit {
			break
		}
	}
	return items, nil
}
