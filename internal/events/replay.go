package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrReplayForbidden      = errors.New("dead letter replay requires admin role")
	ErrDeadLetterNotFound   = errors.New("quarantined dead letter not found")
	ErrOutboxEventNotFound  = errors.New("outbox event not found")
	ErrOutboxEventNotFailed = errors.New("outbox event is not in failed state")
)

type ReplayService struct {
	repository *PostgresRepository
	publisher  Publisher
}

// NewReplayService creates a service that reads dead letters from repository storage and republishes them through publisher.
func NewReplayService(repository *PostgresRepository, publisher Publisher) *ReplayService {
	return &ReplayService{repository: repository, publisher: publisher}
}

// Replay republishes a validated quarantined dead letter for an admin, then marks it replayed; publication can succeed even if the subsequent status update fails.
func (service *ReplayService) Replay(ctx context.Context, deadLetterID, actorRole string) error {
	if actorRole != "admin" {
		return ErrReplayForbidden
	}
	var topic string
	var encoded []byte
	err := service.repository.database.QueryRowContext(ctx, `
		SELECT original_topic, envelope
		FROM eventing.dead_letters
		WHERE id = $1 AND status = 'quarantined'
	`, deadLetterID).Scan(&topic, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeadLetterNotFound
	}
	if err != nil {
		return fmt.Errorf("read dead letter for replay: %w", err)
	}
	var envelope Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return fmt.Errorf("decode dead letter envelope: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return err
	}
	if err := service.publisher.Publish(ctx, OutboxEvent{Topic: topic, Envelope: envelope}); err != nil {
		return err
	}
	result, err := service.repository.database.ExecContext(ctx, `
		UPDATE eventing.dead_letters
		SET status = 'replayed', replay_count = replay_count + 1, replayed_at = $2
		WHERE id = $1 AND status = 'quarantined'
	`, deadLetterID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("mark dead letter replayed: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read dead letter replay result: %w", err)
	}
	if rows != 1 {
		return ErrDeadLetterNotFound
	}
	return nil
}

// ReplayOutboxEvent resets a failed outbox event to pending so the dispatcher
// naturally picks it up again. The API process never publishes directly to a
// broker; delivery stays with the outbox dispatcher.
func (service *ReplayService) ReplayOutboxEvent(ctx context.Context, eventID, actorRole string) error {
	if actorRole != "admin" {
		return ErrReplayForbidden
	}
	now := time.Now().UTC()
	result, err := service.repository.database.ExecContext(ctx, `
		UPDATE eventing.outbox
		SET state = 'pending', attempts = 0, available_at = $2,
		    lease_owner = NULL, lease_until = NULL, last_error = NULL, updated_at = $2
		WHERE id = $1 AND state = 'failed'
	`, eventID, now)
	if err != nil {
		return fmt.Errorf("reset failed outbox event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read outbox replay result: %w", err)
	}
	if rows == 1 {
		return nil
	}
	var state string
	err = service.repository.database.QueryRowContext(ctx, `SELECT state FROM eventing.outbox WHERE id = $1`, eventID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrOutboxEventNotFound
	}
	if err != nil {
		return fmt.Errorf("read outbox event state: %w", err)
	}
	return ErrOutboxEventNotFailed
}
