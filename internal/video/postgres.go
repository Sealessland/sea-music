package video

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PostgresRepository struct {
	database *sql.DB
	outbox   OutboxWriter
}

func NewPostgresRepository(database *sql.DB) *PostgresRepository {
	return &PostgresRepository{database: database}
}

func (repository *PostgresRepository) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return repository.database.BeginTx(ctx, nil)
}

func (repository *PostgresRepository) WithOutbox(writer OutboxWriter) *PostgresRepository {
	repository.outbox = writer
	return repository
}

func (repository *PostgresRepository) CreateDraft(ctx context.Context, creatorID, title, description string) (Video, error) {
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	if title == "" || len(title) > 120 || len(description) > 5000 {
		return Video{}, errors.New("invalid draft metadata")
	}
	var video Video
	err := repository.database.QueryRowContext(ctx, `
		INSERT INTO video.videos (creator_id, title, description)
		VALUES ($1, $2, $3)
		RETURNING id::text, creator_id::text, title, description, state, version, published_at, created_at, updated_at
	`, creatorID, title, description).Scan(
		&video.ID, &video.CreatorID, &video.Title, &video.Description, &video.State,
		&video.Version, &video.PublishedAt, &video.CreatedAt, &video.UpdatedAt,
	)
	if err != nil {
		return Video{}, fmt.Errorf("create video draft: %w", err)
	}
	return video, nil
}

func (repository *PostgresRepository) Get(ctx context.Context, videoID string) (Video, error) {
	var result Video
	err := repository.database.QueryRowContext(ctx, `
		SELECT id::text, creator_id::text, title, description, state, version, published_at, created_at, updated_at
		FROM video.videos WHERE id = $1
	`, videoID).Scan(
		&result.ID, &result.CreatorID, &result.Title, &result.Description, &result.State,
		&result.Version, &result.PublishedAt, &result.CreatedAt, &result.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Video{}, ErrVideoNotFound
	}
	if err != nil {
		return Video{}, fmt.Errorf("get video: %w", err)
	}
	return result, nil
}

func (repository *PostgresRepository) Transition(ctx context.Context, videoID, actorID string, expectedVersion int64, target State, reason string) (Video, error) {
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return Video{}, fmt.Errorf("begin video transition: %w", err)
	}
	defer transaction.Rollback()

	current, err := selectVideoForUpdate(ctx, transaction, videoID)
	if err != nil {
		return Video{}, err
	}
	next, err := current.Transition(expectedVersion, target)
	if err != nil {
		return Video{}, err
	}
	now := time.Now().UTC()
	var publishedAt any
	if target == StatePublished {
		publishedAt = now
	} else {
		publishedAt = current.PublishedAt
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE video.videos
		SET state = $3, version = version + 1, published_at = $4, updated_at = $5
		WHERE id = $1 AND version = $2
	`, videoID, expectedVersion, target, publishedAt, now)
	if err != nil {
		return Video{}, fmt.Errorf("update video state: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Video{}, fmt.Errorf("read video transition result: %w", err)
	}
	if rows != 1 {
		return Video{}, ErrVersionConflict
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO video.state_transitions (video_id, from_state, to_state, actor_id, reason, resulting_version)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, videoID, current.State, target, actorID, reason, next.Version); err != nil {
		return Video{}, fmt.Errorf("audit video transition: %w", err)
	}
	if repository.outbox != nil && (target == StatePublished || target == StateWithdrawn) {
		payload, err := json.Marshal(map[string]any{"video_id": videoID, "state": target, "actor_id": actorID, "reason": reason})
		if err != nil {
			return Video{}, fmt.Errorf("encode video transition event: %w", err)
		}
		eventType := "video." + string(target)
		if _, err := repository.outbox.Enqueue(ctx, transaction, DomainEvent{
			Topic: "domain-events", Type: eventType, Version: 1, AggregateType: "video",
			AggregateID: videoID, AggregateVersion: next.Version, Data: payload,
		}); err != nil {
			return Video{}, fmt.Errorf("enqueue video transition event: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return Video{}, fmt.Errorf("commit video transition: %w", err)
	}
	next.UpdatedAt = now
	if target == StatePublished {
		next.PublishedAt = &now
	}
	return next, nil
}

func (repository *PostgresRepository) BeginUpload(ctx context.Context, request UploadRequest) (SourceAsset, error) {
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return SourceAsset{}, fmt.Errorf("begin source upload: %w", err)
	}
	defer transaction.Rollback()
	video, err := selectVideoForUpdate(ctx, transaction, request.VideoID)
	if err != nil {
		return SourceAsset{}, err
	}
	if video.CreatorID != request.CreatorID {
		return SourceAsset{}, ErrUploadForbidden
	}
	if video.State != StateDraft {
		return SourceAsset{}, ErrInvalidTransition
	}
	objectKey := fmt.Sprintf("sources/%s/%s/source", request.CreatorID, request.VideoID)
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO video.source_assets (video_id, object_key, size_bytes, content_type, checksum_sha256)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (video_id) DO NOTHING
	`, request.VideoID, objectKey, request.SizeBytes, request.ContentType, request.ChecksumSHA256); err != nil {
		return SourceAsset{}, fmt.Errorf("create source upload: %w", err)
	}
	asset, err := selectSourceAsset(ctx, transaction, request.VideoID)
	if err != nil {
		return SourceAsset{}, err
	}
	if asset.SizeBytes != request.SizeBytes || asset.ContentType != request.ContentType || asset.ChecksumSHA256 != request.ChecksumSHA256 || asset.Status == "rejected" {
		return SourceAsset{}, ErrInvalidUpload
	}
	if err := transaction.Commit(); err != nil {
		return SourceAsset{}, fmt.Errorf("commit source upload: %w", err)
	}
	return asset, nil
}

func (repository *PostgresRepository) GetUpload(ctx context.Context, videoID, creatorID string) (SourceAsset, error) {
	var asset SourceAsset
	var ownerID string
	err := repository.database.QueryRowContext(ctx, `
		SELECT a.id::text, a.video_id::text, a.object_key, a.size_bytes, a.content_type, a.checksum_sha256, a.status, v.creator_id::text
		FROM video.source_assets a JOIN video.videos v ON v.id = a.video_id
		WHERE a.video_id = $1
	`, videoID).Scan(&asset.ID, &asset.VideoID, &asset.ObjectKey, &asset.SizeBytes, &asset.ContentType, &asset.ChecksumSHA256, &asset.Status, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceAsset{}, ErrVideoNotFound
	}
	if err != nil {
		return SourceAsset{}, fmt.Errorf("get source upload: %w", err)
	}
	if ownerID != creatorID {
		return SourceAsset{}, ErrUploadForbidden
	}
	if asset.Status == "rejected" {
		return SourceAsset{}, ErrInvalidUpload
	}
	return asset, nil
}

func (repository *PostgresRepository) RejectUpload(ctx context.Context, videoID, creatorID string) error {
	result, err := repository.database.ExecContext(ctx, `
		UPDATE video.source_assets a SET status = 'rejected'
		FROM video.videos v
		WHERE a.video_id = v.id AND a.video_id = $1 AND v.creator_id = $2 AND a.status = 'pending'
	`, videoID, creatorID)
	if err != nil {
		return fmt.Errorf("reject source upload: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read source rejection result: %w", err)
	}
	if rows != 1 {
		return ErrInvalidUpload
	}
	return nil
}

func (repository *PostgresRepository) FinalizeUpload(ctx context.Context, videoID, creatorID string) (FinalizeResult, error) {
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("begin finalize upload: %w", err)
	}
	defer transaction.Rollback()
	current, err := selectVideoForUpdate(ctx, transaction, videoID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if current.CreatorID != creatorID {
		return FinalizeResult{}, ErrUploadForbidden
	}
	asset, err := selectSourceAsset(ctx, transaction, videoID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if asset.Status == "rejected" {
		return FinalizeResult{}, ErrInvalidUpload
	}
	newlyFinalized := asset.Status == "pending"
	if newlyFinalized {
		now := time.Now().UTC()
		if _, err := transaction.ExecContext(ctx, `UPDATE video.source_assets SET status = 'verified', finalized_at = $2 WHERE id = $1`, asset.ID, now); err != nil {
			return FinalizeResult{}, fmt.Errorf("verify source upload: %w", err)
		}
		if current.State != StateDraft {
			return FinalizeResult{}, ErrInvalidTransition
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE video.videos SET state = 'uploaded', version = version + 1, updated_at = $2 WHERE id = $1 AND version = $3`, videoID, now, current.Version); err != nil {
			return FinalizeResult{}, fmt.Errorf("mark video uploaded: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO video.state_transitions (video_id, from_state, to_state, actor_id, reason, resulting_version)
			VALUES ($1, $2, 'uploaded', $3, 'source object verified', $4)
		`, videoID, current.State, creatorID, current.Version+1); err != nil {
			return FinalizeResult{}, fmt.Errorf("audit upload finalization: %w", err)
		}
		current.State = StateUploaded
		current.Version++
		current.UpdatedAt = now
	}
	var jobID string
	err = transaction.QueryRowContext(ctx, `
		INSERT INTO video.processing_jobs (source_asset_id, config_version, state)
		VALUES ($1, 1, 'queued')
		ON CONFLICT (source_asset_id, config_version) DO UPDATE SET source_asset_id = EXCLUDED.source_asset_id
		RETURNING id::text
	`, asset.ID).Scan(&jobID)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("enqueue source processing: %w", err)
	}
	if newlyFinalized && repository.outbox != nil {
		payload, err := json.Marshal(map[string]any{"video_id": videoID, "asset_id": asset.ID, "job_id": jobID, "config_version": 1})
		if err != nil {
			return FinalizeResult{}, fmt.Errorf("encode source finalized event: %w", err)
		}
		eventID, err := repository.outbox.Enqueue(ctx, transaction, DomainEvent{
			Topic: "domain-events", Type: "video.source_finalized", Version: 1,
			AggregateType: "video", AggregateID: videoID, AggregateVersion: current.Version, Data: payload,
		})
		if err != nil {
			return FinalizeResult{}, fmt.Errorf("enqueue source finalized event: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE video.source_assets SET finalized_event_id = $2 WHERE id = $1`, asset.ID, eventID); err != nil {
			return FinalizeResult{}, fmt.Errorf("link source finalized event: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return FinalizeResult{}, fmt.Errorf("commit upload finalization: %w", err)
	}
	return FinalizeResult{AssetID: asset.ID, JobID: jobID, Video: current}, nil
}

func selectSourceAsset(ctx context.Context, transaction *sql.Tx, videoID string) (SourceAsset, error) {
	var asset SourceAsset
	err := transaction.QueryRowContext(ctx, `
		SELECT id::text, video_id::text, object_key, size_bytes, content_type, checksum_sha256, status
		FROM video.source_assets WHERE video_id = $1 FOR UPDATE
	`, videoID).Scan(&asset.ID, &asset.VideoID, &asset.ObjectKey, &asset.SizeBytes, &asset.ContentType, &asset.ChecksumSHA256, &asset.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceAsset{}, ErrVideoNotFound
	}
	if err != nil {
		return SourceAsset{}, fmt.Errorf("lock source upload: %w", err)
	}
	return asset, nil
}

func selectVideoForUpdate(ctx context.Context, transaction *sql.Tx, videoID string) (Video, error) {
	var video Video
	err := transaction.QueryRowContext(ctx, `
		SELECT id::text, creator_id::text, title, description, state, version, published_at, created_at, updated_at
		FROM video.videos
		WHERE id = $1
		FOR UPDATE
	`, videoID).Scan(
		&video.ID, &video.CreatorID, &video.Title, &video.Description, &video.State,
		&video.Version, &video.PublishedAt, &video.CreatedAt, &video.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Video{}, ErrVideoNotFound
	}
	if err != nil {
		return Video{}, fmt.Errorf("lock video: %w", err)
	}
	return video, nil
}
