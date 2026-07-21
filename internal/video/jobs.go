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

var (
	ErrNoProcessingJob     = errors.New("no processing job available")
	ErrProcessingLeaseLost = errors.New("processing job lease lost")
)

type ProcessingJob struct {
	ID            string
	SourceAssetID string
	ConfigVersion int
	State         string
	Attempts      int
	MaxAttempts   int
	LeaseOwner    string
	LeaseUntil    time.Time
	AvailableAt   time.Time
}

// ClaimProcessingJob atomically claims the next available pending or expired-lease job, increments its attempt count, and returns ErrNoProcessingJob when none is eligible.
func (repository *PostgresRepository) ClaimProcessingJob(ctx context.Context, workerID string, leaseDuration time.Duration) (ProcessingJob, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" || leaseDuration <= 0 {
		return ProcessingJob{}, errors.New("invalid processing lease request")
	}
	now := time.Now().UTC()
	leaseUntil := now.Add(leaseDuration)
	var job ProcessingJob
	err := repository.database.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id
			FROM video.processing_jobs
			WHERE attempts < max_attempts
			  AND ((state = 'pending' AND available_at <= $1) OR (state = 'processing' AND lease_until < $1))
			ORDER BY available_at, created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE video.processing_jobs j
		SET state = 'processing', attempts = attempts + 1, lease_owner = $2, lease_until = $3, updated_at = $1
		FROM candidate
		WHERE j.id = candidate.id
		RETURNING j.id::text, j.source_asset_id::text, j.config_version, j.state, j.attempts, j.max_attempts,
		          j.lease_owner, j.lease_until, j.available_at
	`, now, workerID, leaseUntil).Scan(
		&job.ID, &job.SourceAssetID, &job.ConfigVersion, &job.State, &job.Attempts, &job.MaxAttempts,
		&job.LeaseOwner, &job.LeaseUntil, &job.AvailableAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProcessingJob{}, ErrNoProcessingJob
	}
	if err != nil {
		return ProcessingJob{}, fmt.Errorf("claim processing job: %w", err)
	}
	return job, nil
}

// ActivateStaleQueuedJobs promotes processing jobs that stayed queued longer
// than the threshold, covering the loss of the video.source_finalized
// activation event. The state-guarded UPDATE is idempotent and safe to run
// from multiple workers concurrently.
func (repository *PostgresRepository) ActivateStaleQueuedJobs(ctx context.Context, threshold time.Duration) (int64, error) {
	if threshold <= 0 {
		return 0, errors.New("invalid queued activation threshold")
	}
	now := time.Now().UTC()
	result, err := repository.database.ExecContext(ctx, `
		UPDATE video.processing_jobs
		SET state = 'pending', available_at = $1, updated_at = $1
		WHERE state = 'queued' AND updated_at < $2
	`, now, now.Add(-threshold))
	if err != nil {
		return 0, fmt.Errorf("activate stale queued processing jobs: %w", err)
	}
	activated, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read queued activation result: %w", err)
	}
	return activated, nil
}

// RenewProcessingLease extends an unexpired processing lease owned by workerID and returns ErrProcessingLeaseLost if the job is no longer validly leased to that worker.
func (repository *PostgresRepository) RenewProcessingLease(ctx context.Context, jobID, workerID string, leaseDuration time.Duration) (ProcessingJob, error) {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(workerID) == "" || leaseDuration <= 0 {
		return ProcessingJob{}, errors.New("invalid processing lease renewal")
	}
	now := time.Now().UTC()
	var job ProcessingJob
	err := repository.database.QueryRowContext(ctx, `
		UPDATE video.processing_jobs
		SET lease_until = $4, updated_at = $3
		WHERE id = $1 AND state = 'processing' AND lease_owner = $2 AND lease_until > $3
		RETURNING id::text, source_asset_id::text, config_version, state, attempts, max_attempts,
		          lease_owner, lease_until, available_at
	`, jobID, workerID, now, now.Add(leaseDuration)).Scan(
		&job.ID, &job.SourceAssetID, &job.ConfigVersion, &job.State, &job.Attempts, &job.MaxAttempts,
		&job.LeaseOwner, &job.LeaseUntil, &job.AvailableAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProcessingJob{}, ErrProcessingLeaseLost
	}
	if err != nil {
		return ProcessingJob{}, fmt.Errorf("renew processing lease: %w", err)
	}
	return job, nil
}

// FailProcessingJob releases a valid processing lease and either reschedules the job after backoff or, when attempts are exhausted, marks the job failed and atomically transitions a still-processing video to failed with an audit record; it returns ErrProcessingLeaseLost for an invalid or expired lease.
func (repository *PostgresRepository) FailProcessingJob(ctx context.Context, jobID, workerID, failure string, backoff time.Duration) error {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(workerID) == "" || backoff < 0 {
		return errors.New("invalid processing failure")
	}
	failure = strings.TrimSpace(failure)
	if len(failure) > 2000 {
		failure = failure[:2000]
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin processing failure: %w", err)
	}
	defer transaction.Rollback()
	now := time.Now().UTC()
	var attempts, maxAttempts int
	var videoID string
	err = transaction.QueryRowContext(ctx, `
		SELECT j.attempts, j.max_attempts, v.id::text
		FROM video.processing_jobs j
		JOIN video.source_assets a ON a.id = j.source_asset_id
		JOIN video.videos v ON v.id = a.video_id
		WHERE j.id = $1 AND j.state = 'processing' AND j.lease_owner = $2 AND j.lease_until > $3
		FOR UPDATE OF j, v
	`, jobID, workerID, now).Scan(&attempts, &maxAttempts, &videoID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrProcessingLeaseLost
	}
	if err != nil {
		return fmt.Errorf("lock failed processing job: %w", err)
	}
	exhausted := attempts >= maxAttempts
	nextState := "pending"
	if exhausted {
		nextState = "failed"
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE video.processing_jobs
		SET state = $2, lease_owner = NULL, lease_until = NULL, available_at = $3, last_error = $4, updated_at = $5
		WHERE id = $1
	`, jobID, nextState, now.Add(backoff), failure, now); err != nil {
		return fmt.Errorf("fail processing job: %w", err)
	}
	if exhausted {
		current, err := selectVideoForUpdate(ctx, transaction, videoID)
		if err != nil {
			return err
		}
		if current.State == StateProcessing {
			if _, err := transaction.ExecContext(ctx, `UPDATE video.videos SET state = 'failed', version = version + 1, updated_at = $2 WHERE id = $1`, videoID, now); err != nil {
				return fmt.Errorf("mark video processing failed: %w", err)
			}
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO video.state_transitions (video_id, from_state, to_state, reason, resulting_version)
				VALUES ($1, $2, 'failed', $3, $4)
			`, videoID, current.State, "processing retry budget exhausted", current.Version+1); err != nil {
				return fmt.Errorf("audit video processing failure: %w", err)
			}
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit processing failure: %w", err)
	}
	return nil
}

// StartProcessingJob validates and locks the worker's live job lease, then atomically transitions an uploaded video to processing with an audit record or accepts one already processing; other video states return ErrInvalidTransition.
func (repository *PostgresRepository) StartProcessingJob(ctx context.Context, jobID, workerID string) (ProcessingInput, error) {
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return ProcessingInput{}, fmt.Errorf("begin processing job: %w", err)
	}
	defer transaction.Rollback()
	now := time.Now().UTC()
	input, err := lockProcessingInput(ctx, transaction, jobID, workerID, now)
	if err != nil {
		return ProcessingInput{}, err
	}
	current, err := selectVideoForUpdate(ctx, transaction, input.VideoID)
	if err != nil {
		return ProcessingInput{}, err
	}
	if current.State == StateUploaded {
		if _, err := transaction.ExecContext(ctx, `UPDATE video.videos SET state = 'processing', version = version + 1, updated_at = $2 WHERE id = $1`, input.VideoID, now); err != nil {
			return ProcessingInput{}, fmt.Errorf("mark video processing: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO video.state_transitions (video_id, from_state, to_state, reason, resulting_version)
			VALUES ($1, $2, 'processing', 'media worker started', $3)
		`, input.VideoID, current.State, current.Version+1); err != nil {
			return ProcessingInput{}, fmt.Errorf("audit processing start: %w", err)
		}
	} else if current.State != StateProcessing {
		return ProcessingInput{}, ErrInvalidTransition
	}
	if err := transaction.Commit(); err != nil {
		return ProcessingInput{}, fmt.Errorf("commit processing start: %w", err)
	}
	return input, nil
}

// CompleteProcessingJob validates and upserts nonempty rendition output under a live worker lease, marks the job succeeded, atomically moves the processing video to review with an audit record, and enqueues a moderation-ready event when an outbox is available.
func (repository *PostgresRepository) CompleteProcessingJob(ctx context.Context, jobID, workerID string, renditions []Rendition) (ProcessingResult, error) {
	if len(renditions) == 0 {
		return ProcessingResult{}, errors.New("processing produced no renditions")
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return ProcessingResult{}, fmt.Errorf("begin processing completion: %w", err)
	}
	defer transaction.Rollback()
	now := time.Now().UTC()
	input, err := lockProcessingInput(ctx, transaction, jobID, workerID, now)
	if err != nil {
		return ProcessingResult{}, err
	}
	current, err := selectVideoForUpdate(ctx, transaction, input.VideoID)
	if err != nil {
		return ProcessingResult{}, err
	}
	if current.State != StateProcessing {
		return ProcessingResult{}, ErrInvalidTransition
	}
	for _, rendition := range renditions {
		if rendition.Kind == "" || rendition.ObjectKey == "" || rendition.ConfigVersion != input.Job.ConfigVersion {
			return ProcessingResult{}, errors.New("invalid rendition output")
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO video.renditions (source_asset_id, config_version, kind, object_key, status, width, height, completed_at)
			VALUES ($1, $2, $3, $4, 'ready', $5, $6, $7)
			ON CONFLICT (source_asset_id, config_version, kind)
			DO UPDATE SET object_key = EXCLUDED.object_key, status = 'ready', width = EXCLUDED.width,
			              height = EXCLUDED.height, completed_at = EXCLUDED.completed_at
		`, input.Job.SourceAssetID, rendition.ConfigVersion, rendition.Kind, rendition.ObjectKey, rendition.Width, rendition.Height, now); err != nil {
			return ProcessingResult{}, fmt.Errorf("record rendition: %w", err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `
		UPDATE video.processing_jobs
		SET state = 'succeeded', lease_owner = NULL, lease_until = NULL, last_error = NULL, updated_at = $2
		WHERE id = $1
	`, jobID, now); err != nil {
		return ProcessingResult{}, fmt.Errorf("complete processing job: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE video.videos SET state = 'review', version = version + 1, updated_at = $2 WHERE id = $1`, input.VideoID, now); err != nil {
		return ProcessingResult{}, fmt.Errorf("mark video ready for review: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO video.state_transitions (video_id, from_state, to_state, reason, resulting_version)
		VALUES ($1, $2, 'review', 'media renditions completed', $3)
	`, input.VideoID, current.State, current.Version+1); err != nil {
		return ProcessingResult{}, fmt.Errorf("audit processing completion: %w", err)
	}
	if repository.outbox != nil {
		payload, err := json.Marshal(map[string]any{
			"video_id": input.VideoID, "source_asset_id": input.Job.SourceAssetID,
			"config_version": input.Job.ConfigVersion, "video_version": current.Version + 1,
		})
		if err != nil {
			return ProcessingResult{}, fmt.Errorf("encode moderation-ready event: %w", err)
		}
		if _, err := repository.outbox.Enqueue(ctx, transaction, DomainEvent{
			Topic: "domain-events", Type: "video.ready_for_moderation", Version: 1,
			AggregateType: "video", AggregateID: input.VideoID, AggregateVersion: current.Version + 1, Data: payload,
		}); err != nil {
			return ProcessingResult{}, fmt.Errorf("enqueue moderation-ready event: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return ProcessingResult{}, fmt.Errorf("commit processing completion: %w", err)
	}
	current.State = StateReview
	current.Version++
	current.UpdatedAt = now
	return ProcessingResult{Video: current, Renditions: renditions}, nil
}

// lockProcessingInput locks and returns the processing job and source-asset input only while workerID owns an unexpired lease, returning ErrProcessingLeaseLost otherwise.
func lockProcessingInput(ctx context.Context, transaction *sql.Tx, jobID, workerID string, now time.Time) (ProcessingInput, error) {
	var input ProcessingInput
	err := transaction.QueryRowContext(ctx, `
		SELECT j.id::text, j.source_asset_id::text, j.config_version, j.state, j.attempts, j.max_attempts,
		       j.lease_owner, j.lease_until, j.available_at, a.video_id::text, a.object_key
		FROM video.processing_jobs j
		JOIN video.source_assets a ON a.id = j.source_asset_id
		WHERE j.id = $1 AND j.state = 'processing' AND j.lease_owner = $2 AND j.lease_until > $3
		FOR UPDATE OF j
	`, jobID, workerID, now).Scan(
		&input.Job.ID, &input.Job.SourceAssetID, &input.Job.ConfigVersion, &input.Job.State,
		&input.Job.Attempts, &input.Job.MaxAttempts, &input.Job.LeaseOwner, &input.Job.LeaseUntil,
		&input.Job.AvailableAt, &input.VideoID, &input.ObjectKey,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProcessingInput{}, ErrProcessingLeaseLost
	}
	if err != nil {
		return ProcessingInput{}, fmt.Errorf("lock processing input: %w", err)
	}
	return input, nil
}
