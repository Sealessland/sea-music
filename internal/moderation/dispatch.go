package moderation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Remote interface {
	StartReview(context.Context, ReviewRequest) (Operation, error)
	GetReview(context.Context, string) (Operation, error)
}

type DispatchJob struct {
	ID           string
	EventID      string
	VideoID      string
	VideoVersion int64
	OperationID  string
}

type Dispatcher struct {
	database      *sql.DB
	remote        Remote
	workerID      string
	bucket        string
	policyVersion string
	mode          Mode
	leaseDuration time.Duration
	pollInterval  time.Duration
}

func NewDispatcher(database *sql.DB, remote Remote, workerID, bucket, policyVersion string, mode Mode, leaseDuration, pollInterval time.Duration) *Dispatcher {
	return &Dispatcher{database: database, remote: remote, workerID: workerID, bucket: bucket, policyVersion: policyVersion, mode: mode, leaseDuration: leaseDuration, pollInterval: pollInterval}
}

func EnqueueDispatchTx(ctx context.Context, transaction *sql.Tx, eventID, videoID string, videoVersion int64) error {
	if transaction == nil || strings.TrimSpace(eventID) == "" || strings.TrimSpace(videoID) == "" || videoVersion < 0 {
		return ErrInvalidRequest
	}
	_, err := transaction.ExecContext(ctx, `
		INSERT INTO moderation.dispatch_jobs (event_id, video_id, video_version)
		VALUES ($1, $2, $3)
		ON CONFLICT (event_id) DO NOTHING
	`, eventID, videoID, videoVersion)
	if err != nil {
		return fmt.Errorf("enqueue moderation dispatch: %w", err)
	}
	return nil
}

func (dispatcher *Dispatcher) RunOnce(ctx context.Context) (DispatchJob, error) {
	if err := dispatcher.validate(); err != nil {
		return DispatchJob{}, err
	}
	job, err := dispatcher.claim(ctx)
	if err != nil {
		return DispatchJob{}, err
	}
	var operation Operation
	if job.OperationID == "" {
		request, requestErr := dispatcher.buildRequest(ctx, job)
		if requestErr != nil {
			return DispatchJob{}, dispatcher.retry(ctx, job, requestErr)
		}
		operation, err = dispatcher.remote.StartReview(ctx, request)
	} else {
		operation, err = dispatcher.remote.GetReview(ctx, job.OperationID)
	}
	if err != nil {
		return DispatchJob{}, dispatcher.retry(ctx, job, err)
	}
	if operation.ID == "" {
		return DispatchJob{}, dispatcher.retry(ctx, job, errors.New("moderation agent returned an empty operation id"))
	}
	switch operation.Status {
	case StatusPending, StatusRunning:
		err = dispatcher.await(ctx, job, operation.ID)
	case StatusCompleted:
		if operation.Result == nil {
			cause := errors.New("completed moderation operation has no result")
			return DispatchJob{}, dispatcher.retry(ctx, job, cause)
		} else {
			err = dispatcher.complete(ctx, job, operation)
		}
	case StatusFailed, StatusCancelled:
		err = dispatcher.failPermanent(ctx, job, operation.ID, operation.Error)
	default:
		cause := errors.New("moderation agent returned an invalid operation status")
		return DispatchJob{}, dispatcher.retry(ctx, job, cause)
	}
	if err != nil {
		return DispatchJob{}, err
	}
	job.OperationID = operation.ID
	return job, nil
}

func (dispatcher *Dispatcher) validate() error {
	if dispatcher == nil || dispatcher.database == nil || dispatcher.remote == nil || strings.TrimSpace(dispatcher.workerID) == "" ||
		strings.TrimSpace(dispatcher.bucket) == "" || strings.TrimSpace(dispatcher.policyVersion) == "" ||
		(dispatcher.mode != ModeShadow && dispatcher.mode != ModeEnforce) || dispatcher.leaseDuration <= 0 || dispatcher.pollInterval <= 0 {
		return errors.New("invalid moderation dispatcher configuration")
	}
	return nil
}

func (dispatcher *Dispatcher) claim(ctx context.Context) (DispatchJob, error) {
	var job DispatchJob
	err := dispatcher.database.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id FROM moderation.dispatch_jobs
			WHERE failures < max_failures AND ((state = 'pending' AND available_at <= now()) OR (state = 'dispatching' AND lease_until <= now()))
			ORDER BY available_at, created_at FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE moderation.dispatch_jobs job
		SET state = 'dispatching', lease_owner = $1, lease_until = now() + $2 * interval '1 microsecond', updated_at = now()
		FROM candidate WHERE job.id = candidate.id
		RETURNING job.id::text, job.event_id::text, job.video_id::text, job.video_version, COALESCE(job.operation_id::text, '')
	`, dispatcher.workerID, dispatcher.leaseDuration.Microseconds()).Scan(&job.ID, &job.EventID, &job.VideoID, &job.VideoVersion, &job.OperationID)
	if errors.Is(err, sql.ErrNoRows) {
		return DispatchJob{}, ErrNoOperation
	}
	if err != nil {
		return DispatchJob{}, fmt.Errorf("claim moderation dispatch: %w", err)
	}
	return job, nil
}

func (dispatcher *Dispatcher) buildRequest(ctx context.Context, job DispatchJob) (ReviewRequest, error) {
	var title, description, objectKey, checksum, mediaType string
	err := dispatcher.database.QueryRowContext(ctx, `
		SELECT v.title, v.description, a.object_key, a.checksum_sha256, COALESCE(a.content_type, 'application/octet-stream')
		FROM video.videos v JOIN video.source_assets a ON a.video_id = v.id
		WHERE v.id = $1 AND v.version = $2 AND v.state = 'review' AND a.status = 'verified'
	`, job.VideoID, job.VideoVersion).Scan(&title, &description, &objectKey, &checksum, &mediaType)
	if err != nil {
		return ReviewRequest{}, fmt.Errorf("load moderation dispatch input: %w", err)
	}
	return ReviewRequest{
		RequestID: job.EventID, VideoID: job.VideoID, VideoVersion: job.VideoVersion,
		PolicyVersion: dispatcher.policyVersion, Mode: dispatcher.mode, Title: title, Description: description,
		Assets: []Asset{{Kind: "source", URI: "s3://" + dispatcher.bucket + "/" + objectKey, SHA256: checksum, MediaType: mediaType}},
	}, nil
}

func (dispatcher *Dispatcher) await(ctx context.Context, job DispatchJob, operationID string) error {
	return dispatcher.updateLease(ctx, job, `
		UPDATE moderation.dispatch_jobs SET state = 'pending', operation_id = $3, available_at = now() + $4 * interval '1 microsecond',
		lease_owner = NULL, lease_until = NULL, last_error = NULL, updated_at = now()
		WHERE id = $1 AND state = 'dispatching' AND lease_owner = $2 AND lease_until > now()
	`, operationID, dispatcher.pollInterval.Microseconds())
}

func (dispatcher *Dispatcher) complete(ctx context.Context, job DispatchJob, operation Operation) error {
	encoded, err := json.Marshal(operation.Result)
	if err != nil {
		return err
	}
	return dispatcher.updateLease(ctx, job, `
		UPDATE moderation.dispatch_jobs SET state = 'completed', operation_id = $3, result = $4, completed_at = now(),
		lease_owner = NULL, lease_until = NULL, last_error = NULL, updated_at = now()
		WHERE id = $1 AND state = 'dispatching' AND lease_owner = $2 AND lease_until > now()
	`, operation.ID, encoded)
}

func (dispatcher *Dispatcher) retry(ctx context.Context, job DispatchJob, cause error) error {
	message := trimError(cause)
	err := dispatcher.updateLease(ctx, job, `
		UPDATE moderation.dispatch_jobs SET failures = failures + 1,
		state = CASE WHEN failures + 1 >= max_failures THEN 'failed' ELSE 'pending' END,
		available_at = now() + $3 * interval '1 microsecond', last_error = $4,
		lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND state = 'dispatching' AND lease_owner = $2 AND lease_until > now()
	`, dispatcher.pollInterval.Microseconds(), message)
	return errors.Join(cause, err)
}

func (dispatcher *Dispatcher) failPermanent(ctx context.Context, job DispatchJob, operationID, message string) error {
	return dispatcher.updateLease(ctx, job, `
		UPDATE moderation.dispatch_jobs SET state = 'failed', operation_id = NULLIF($3, '')::uuid, last_error = $4,
		lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND state = 'dispatching' AND lease_owner = $2 AND lease_until > now()
	`, operationID, trimText(message))
}

func (dispatcher *Dispatcher) updateLease(ctx context.Context, job DispatchJob, query string, arguments ...any) error {
	params := []any{job.ID, dispatcher.workerID}
	params = append(params, arguments...)
	result, err := dispatcher.database.ExecContext(ctx, query, params...)
	if err != nil {
		return fmt.Errorf("update moderation dispatch: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

func trimError(err error) string {
	if err == nil {
		return "unknown moderation dispatch error"
	}
	return trimText(err.Error())
}

func trimText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 2000 {
		value = value[:2000]
	}
	return value
}
