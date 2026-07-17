package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/redis/go-redis/v9"
)

type ReconciliationResult struct {
	Before     VideoCounters `json:"before"`
	After      VideoCounters `json:"after"`
	DriftTotal int64         `json:"drift_total"`
	Repaired   bool          `json:"repaired"`
}

type CounterReconciler struct {
	database *sql.DB
	redis    redis.UniversalClient
}

type ReconciliationStats struct {
	Repairs    int64
	DriftTotal int64
}

func (reconciler *CounterReconciler) Stats(ctx context.Context) (ReconciliationStats, error) {
	var stats ReconciliationStats
	if err := reconciler.database.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(drift_total), 0) FROM social.counter_reconciliations
	`).Scan(&stats.Repairs, &stats.DriftTotal); err != nil {
		return ReconciliationStats{}, fmt.Errorf("read reconciliation stats: %w", err)
	}
	return stats, nil
}

func NewCounterReconciler(database *sql.DB, client redis.UniversalClient) *CounterReconciler {
	return &CounterReconciler{database: database, redis: client}
}

func (reconciler *CounterReconciler) Reconcile(ctx context.Context, videoID string) (ReconciliationResult, error) {
	transaction, err := reconciler.database.BeginTx(ctx, nil)
	if err != nil {
		return ReconciliationResult{}, fmt.Errorf("begin counter reconciliation: %w", err)
	}
	defer transaction.Rollback()
	before := VideoCounters{VideoID: videoID}
	err = transaction.QueryRowContext(ctx, `
		SELECT video_id::text, likes, favorites, comments, danmaku
		FROM social.video_counters WHERE video_id = $1 FOR UPDATE
	`, videoID).Scan(&before.VideoID, &before.Likes, &before.Favorites, &before.Comments, &before.Danmaku)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ReconciliationResult{}, fmt.Errorf("lock counter snapshot: %w", err)
	}
	after := VideoCounters{VideoID: videoID}
	if err := transaction.QueryRowContext(ctx, `
		SELECT
			(SELECT count(*) FROM social.video_likes WHERE video_id = $1),
			(SELECT count(*) FROM social.video_favorites WHERE video_id = $1),
			(SELECT count(*) FROM social.comments WHERE video_id = $1 AND deleted_at IS NULL),
			(SELECT count(*) FROM social.danmaku WHERE video_id = $1 AND visible)
	`, videoID).Scan(&after.Likes, &after.Favorites, &after.Comments, &after.Danmaku); err != nil {
		return ReconciliationResult{}, fmt.Errorf("count authoritative interactions: %w", err)
	}
	drift := absolute(before.Likes-after.Likes) + absolute(before.Favorites-after.Favorites) +
		absolute(before.Comments-after.Comments) + absolute(before.Danmaku-after.Danmaku)
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO social.video_counters (video_id, likes, favorites, comments, danmaku)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (video_id) DO UPDATE
		SET likes = EXCLUDED.likes, favorites = EXCLUDED.favorites, comments = EXCLUDED.comments,
		    danmaku = EXCLUDED.danmaku, updated_at = now()
	`, videoID, after.Likes, after.Favorites, after.Comments, after.Danmaku); err != nil {
		return ReconciliationResult{}, fmt.Errorf("repair counter snapshot: %w", err)
	}
	if drift > 0 {
		previousJSON, _ := json.Marshal(before)
		authorityJSON, _ := json.Marshal(after)
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO social.counter_reconciliations (video_id, previous_counts, authoritative_counts, drift_total)
			VALUES ($1, $2, $3, $4)
		`, videoID, previousJSON, authorityJSON, drift); err != nil {
			return ReconciliationResult{}, fmt.Errorf("audit counter reconciliation: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return ReconciliationResult{}, fmt.Errorf("commit counter reconciliation: %w", err)
	}
	if reconciler.redis != nil {
		if err := reconciler.redis.HSet(ctx, "sea:counters:"+videoID, map[string]any{
			"likes": after.Likes, "favorites": after.Favorites, "comments": after.Comments, "danmaku": after.Danmaku,
		}).Err(); err != nil {
			return ReconciliationResult{}, fmt.Errorf("repair cached counters: %w", err)
		}
	}
	return ReconciliationResult{Before: before, After: after, DriftTotal: drift, Repaired: drift > 0}, nil
}

func (reconciler *CounterReconciler) ReconcileBatch(ctx context.Context, limit int) (int, int64, error) {
	if limit <= 0 || limit > 1000 {
		return 0, 0, errors.New("invalid reconciliation batch size")
	}
	rows, err := reconciler.database.QueryContext(ctx, `
		SELECT id::text FROM video.videos ORDER BY updated_at LIMIT $1
	`, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("list videos for reconciliation: %w", err)
	}
	var videoIDs []string
	for rows.Next() {
		var videoID string
		if err := rows.Scan(&videoID); err != nil {
			rows.Close()
			return 0, 0, err
		}
		videoIDs = append(videoIDs, videoID)
	}
	rows.Close()
	var drift int64
	for _, videoID := range videoIDs {
		result, err := reconciler.Reconcile(ctx, videoID)
		if err != nil {
			return len(videoIDs), drift, err
		}
		drift += result.DriftTotal
	}
	return len(videoIDs), drift, nil
}

func absolute(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
