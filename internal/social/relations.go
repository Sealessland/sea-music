package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidRelation = errors.New("invalid relation")

type RelationResult struct {
	Changed bool  `json:"changed"`
	Exists  bool  `json:"exists"`
	Version int64 `json:"version,omitempty"`
}

type PostgresRepository struct {
	database *sql.DB
	outbox   OutboxWriter
}

// NewPostgresRepository creates a relation repository backed by database; outbox event delivery remains disabled until WithOutbox is called.
func NewPostgresRepository(database *sql.DB) *PostgresRepository {
	return &PostgresRepository{database: database}
}

// WithOutbox sets the writer used to enqueue relation-change events in the same transaction and returns the repository for chaining.
func (repository *PostgresRepository) WithOutbox(writer OutboxWriter) *PostgresRepository {
	repository.outbox = writer
	return repository
}

// SetLike idempotently enables or disables a user's like on a published video, versioning and optionally emitting an event only when the stored relation changes.
func (repository *PostgresRepository) SetLike(ctx context.Context, userID, videoID string, enabled bool) (RelationResult, error) {
	return repository.setRelation(ctx, "like", userID, videoID, enabled, true,
		`INSERT INTO social.video_likes (user_id, video_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		`DELETE FROM social.video_likes WHERE user_id = $1 AND video_id = $2`,
	)
}

// SetFavorite idempotently enables or disables a user's favorite on a published video, versioning and optionally emitting an event only when the stored relation changes.
func (repository *PostgresRepository) SetFavorite(ctx context.Context, userID, videoID string, enabled bool) (RelationResult, error) {
	return repository.setRelation(ctx, "favorite", userID, videoID, enabled, true,
		`INSERT INTO social.video_favorites (user_id, video_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		`DELETE FROM social.video_favorites WHERE user_id = $1 AND video_id = $2`,
	)
}

// SetFollow idempotently enables or disables a follow relation, rejecting attempts to follow oneself and versioning and optionally emitting an event only on change.
func (repository *PostgresRepository) SetFollow(ctx context.Context, followerID, followeeID string, enabled bool) (RelationResult, error) {
	if followerID == followeeID {
		return RelationResult{}, errors.New("users cannot follow themselves")
	}
	return repository.setRelation(ctx, "follow", followerID, followeeID, enabled, false,
		`INSERT INTO social.follows (follower_id, followee_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		`DELETE FROM social.follows WHERE follower_id = $1 AND followee_id = $2`,
	)
}

// setRelation transactionally applies an idempotent relation change, optionally requiring a published video, and versions the relation, enqueuing an outbox event (if configured) only when a row changes.
func (repository *PostgresRepository) setRelation(ctx context.Context, kind, actorID, targetID string, enabled, requirePublishedVideo bool, insertSQL, deleteSQL string) (RelationResult, error) {
	if actorID == "" || targetID == "" {
		return RelationResult{}, errors.New("relation actor and target are required")
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return RelationResult{}, fmt.Errorf("begin %s relation: %w", kind, err)
	}
	defer transaction.Rollback()
	if enabled && requirePublishedVideo {
		var published bool
		if err := transaction.QueryRowContext(ctx, `SELECT state = 'published' FROM video.videos WHERE id = $1`, targetID).Scan(&published); err != nil || !published {
			return RelationResult{}, ErrInvalidRelation
		}
	}
	statement := deleteSQL
	if enabled {
		statement = insertSQL
	}
	result, err := transaction.ExecContext(ctx, statement, actorID, targetID)
	if err != nil {
		return RelationResult{}, fmt.Errorf("set %s relation: %w", kind, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return RelationResult{}, fmt.Errorf("read %s relation result: %w", kind, err)
	}
	if rows == 0 {
		return RelationResult{Exists: enabled}, nil
	}
	var version int64
	if err := transaction.QueryRowContext(ctx, `
		INSERT INTO social.relation_versions (relation_kind, actor_id, target_id, version)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT (relation_kind, actor_id, target_id)
		DO UPDATE SET version = social.relation_versions.version + 1, updated_at = now()
		RETURNING version
	`, kind, actorID, targetID).Scan(&version); err != nil {
		return RelationResult{}, fmt.Errorf("version %s relation: %w", kind, err)
	}
	if repository.outbox != nil {
		payload, err := json.Marshal(map[string]any{"actor_id": actorID, "target_id": targetID, "exists": enabled, "relation": kind})
		if err != nil {
			return RelationResult{}, fmt.Errorf("encode %s relation event: %w", kind, err)
		}
		if _, err := repository.outbox.Enqueue(ctx, transaction, DomainEvent{
			Topic: "domain-events", Type: "social." + kind + ".changed", Version: 1,
			AggregateType: kind, AggregateID: targetID, AggregateVersion: version, Data: payload,
		}); err != nil {
			return RelationResult{}, fmt.Errorf("enqueue %s relation event: %w", kind, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return RelationResult{}, fmt.Errorf("commit %s relation: %w", kind, err)
	}
	return RelationResult{Changed: true, Exists: enabled, Version: version}, nil
}
