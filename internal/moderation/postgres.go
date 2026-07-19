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

type PostgresStore struct {
	database *sql.DB
}

func (store *PostgresStore) Claim(ctx context.Context, workerID string, leaseDuration time.Duration) (Operation, error) {
	if store == nil || store.database == nil || strings.TrimSpace(workerID) == "" || leaseDuration <= 0 {
		return Operation{}, errors.New("invalid moderation operation claim")
	}
	operation, err := scanOperation(store.database.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id
			FROM moderation.review_operations
			WHERE attempts < max_attempts
			  AND ((status = 'pending' AND available_at <= now())
			       OR (status = 'running' AND lease_until <= now()))
			ORDER BY available_at, created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE moderation.review_operations operation
		SET status = 'running', attempts = attempts + 1, lease_owner = $1,
		    lease_until = now() + $2 * interval '1 microsecond', error = NULL, updated_at = now()
		FROM candidate
		WHERE operation.id = candidate.id
		RETURNING operation.id::text, operation.input_hash, operation.request,
		          operation.status, operation.result, operation.error
	`, workerID, leaseDuration.Microseconds()))
	if errors.Is(err, ErrOperationNotFound) {
		return Operation{}, ErrNoOperation
	}
	return operation, err
}

func (store *PostgresStore) Fail(ctx context.Context, operationID, workerID string, cause error, backoff time.Duration) error {
	if store == nil || store.database == nil || strings.TrimSpace(operationID) == "" || strings.TrimSpace(workerID) == "" || cause == nil || backoff < 0 {
		return errors.New("invalid moderation operation failure")
	}
	message := strings.TrimSpace(cause.Error())
	if len(message) > 2000 {
		message = message[:2000]
	}
	result, err := store.database.ExecContext(ctx, `
		UPDATE moderation.review_operations
		SET status = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
		    available_at = now() + $3 * interval '1 microsecond', error = $4,
		    lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'running' AND lease_owner = $2 AND lease_until > now()
	`, operationID, workerID, backoff.Microseconds(), message)
	if err != nil {
		return fmt.Errorf("fail moderation operation: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read moderation operation failure result: %w", err)
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

func NewPostgresStore(database *sql.DB) *PostgresStore {
	return &PostgresStore{database: database}
}

func (store *PostgresStore) Create(ctx context.Context, request ReviewRequest, inputHash string) (Operation, error) {
	if store == nil || store.database == nil {
		return Operation{}, errors.New("moderation database is required")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return Operation{}, fmt.Errorf("encode moderation operation request: %w", err)
	}
	var operationID string
	err = store.database.QueryRowContext(ctx, `
		INSERT INTO moderation.review_operations (request_id, input_hash, request)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_id) DO NOTHING
		RETURNING id::text
	`, request.RequestID, inputHash, encoded).Scan(&operationID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Operation{}, fmt.Errorf("create moderation operation: %w", err)
	}
	if errors.Is(err, sql.ErrNoRows) {
		existing, getErr := store.getByRequestID(ctx, request.RequestID)
		if getErr != nil {
			return Operation{}, getErr
		}
		if existing.InputHash != inputHash {
			return Operation{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	return store.Get(ctx, operationID)
}

func (store *PostgresStore) Get(ctx context.Context, operationID string) (Operation, error) {
	if store == nil || store.database == nil {
		return Operation{}, errors.New("moderation database is required")
	}
	return scanOperation(store.database.QueryRowContext(ctx, `
		SELECT id::text, input_hash, request, status, result, error
		FROM moderation.review_operations
		WHERE id = $1
	`, operationID))
}

func (store *PostgresStore) Complete(ctx context.Context, operationID string, result Result) (Operation, error) {
	if store == nil || store.database == nil {
		return Operation{}, errors.New("moderation database is required")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return Operation{}, fmt.Errorf("encode moderation result: %w", err)
	}
	operation, err := scanOperation(store.database.QueryRowContext(ctx, `
		UPDATE moderation.review_operations
		SET status = 'completed', result = $2, error = NULL, completed_at = now(),
		    lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')
		RETURNING id::text, input_hash, request, status, result, error
	`, operationID, encoded))
	if err == nil {
		return operation, nil
	}
	if !errors.Is(err, ErrOperationNotFound) {
		return Operation{}, err
	}
	existing, getErr := store.Get(ctx, operationID)
	if getErr != nil {
		return Operation{}, getErr
	}
	if existing.Status == StatusCompleted {
		return existing, nil
	}
	return Operation{}, fmt.Errorf("%w: operation status %s", ErrInvalidResult, existing.Status)
}

func (store *PostgresStore) CompleteClaimed(ctx context.Context, operationID, workerID string, result Result) (Operation, error) {
	if store == nil || store.database == nil || strings.TrimSpace(operationID) == "" || strings.TrimSpace(workerID) == "" {
		return Operation{}, errors.New("invalid moderation operation completion")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return Operation{}, fmt.Errorf("encode moderation result: %w", err)
	}
	operation, err := scanOperation(store.database.QueryRowContext(ctx, `
		UPDATE moderation.review_operations
		SET status = 'completed', result = $3, error = NULL, completed_at = now(),
		    lease_owner = NULL, lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status = 'running' AND lease_owner = $2 AND lease_until > now()
		RETURNING id::text, input_hash, request, status, result, error
	`, operationID, workerID, encoded))
	if errors.Is(err, ErrOperationNotFound) {
		return Operation{}, ErrLeaseLost
	}
	return operation, err
}

func (store *PostgresStore) getByRequestID(ctx context.Context, requestID string) (Operation, error) {
	return scanOperation(store.database.QueryRowContext(ctx, `
		SELECT id::text, input_hash, request, status, result, error
		FROM moderation.review_operations
		WHERE request_id = $1
	`, requestID))
}

type rowScanner interface {
	Scan(...any) error
}

func scanOperation(row rowScanner) (Operation, error) {
	var operation Operation
	var encodedRequest []byte
	var encodedResult []byte
	var operationError sql.NullString
	if err := row.Scan(&operation.ID, &operation.InputHash, &encodedRequest, &operation.Status, &encodedResult, &operationError); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Operation{}, ErrOperationNotFound
		}
		return Operation{}, fmt.Errorf("scan moderation operation: %w", err)
	}
	if err := json.Unmarshal(encodedRequest, &operation.Request); err != nil {
		return Operation{}, fmt.Errorf("decode moderation operation request: %w", err)
	}
	if len(encodedResult) > 0 {
		var result Result
		if err := json.Unmarshal(encodedResult, &result); err != nil {
			return Operation{}, fmt.Errorf("decode moderation operation result: %w", err)
		}
		operation.Result = &result
	}
	if operationError.Valid {
		operation.Error = operationError.String
	}
	return operation, nil
}
