package video

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type DomainEvent struct {
	Topic            string
	Type             string
	Version          int
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	Data             json.RawMessage
}

type OutboxWriter interface {
	Enqueue(context.Context, *sql.Tx, DomainEvent) (string, error)
}

type OutboxWriterFunc func(context.Context, *sql.Tx, DomainEvent) (string, error)

// Enqueue delegates writing event to the outbox to the wrapped function and returns its identifier or error unchanged.
func (function OutboxWriterFunc) Enqueue(ctx context.Context, transaction *sql.Tx, event DomainEvent) (string, error) {
	return function(ctx, transaction, event)
}

// ActivateProcessingJobTx atomically moves a queued processing job to pending and makes it immediately available; it is idempotent for jobs already pending, processing, or succeeded, and errors for other states or missing jobs.
func ActivateProcessingJobTx(ctx context.Context, transaction *sql.Tx, jobID string) error {
	result, err := transaction.ExecContext(ctx, `
		UPDATE video.processing_jobs
		SET state = 'pending', available_at = now(), updated_at = now()
		WHERE id = $1 AND state = 'queued'
	`, jobID)
	if err != nil {
		return fmt.Errorf("activate event-driven processing job: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read processing activation result: %w", err)
	}
	if rows == 0 {
		var state string
		if err := transaction.QueryRowContext(ctx, `SELECT state FROM video.processing_jobs WHERE id = $1`, jobID).Scan(&state); err != nil {
			return fmt.Errorf("read processing job activation state: %w", err)
		}
		if state == "pending" || state == "processing" || state == "succeeded" {
			return nil
		}
		return errors.New("processing job cannot be activated")
	}
	return nil
}
