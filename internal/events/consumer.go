package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ConsumerConfig defines broker-independent processing semantics. Broker
// endpoints and credentials belong to BrokerConfig, not this type.
type ConsumerConfig struct {
	Topic       string
	Group       string
	Name        string
	MaxAttempts int
	BaseBackoff time.Duration
}

// consumerRuntime owns the behavior every broker adapter must preserve:
// validate, process through Inbox, retry, then transactionally quarantine.
type consumerRuntime struct {
	config     ConsumerConfig
	inbox      *Inbox
	repository *PostgresRepository
}

func newConsumerRuntime(config ConsumerConfig, inbox *Inbox, repository *PostgresRepository) (consumerRuntime, error) {
	if strings.TrimSpace(config.Topic) == "" || strings.TrimSpace(config.Group) == "" ||
		strings.TrimSpace(config.Name) == "" || config.MaxAttempts <= 0 || config.BaseBackoff <= 0 ||
		inbox == nil || repository == nil {
		return consumerRuntime{}, errors.New("invalid event consumer configuration")
	}
	return consumerRuntime{config: config, inbox: inbox, repository: repository}, nil
}

func (runtime consumerRuntime) process(ctx context.Context, originalTopic string, envelope Envelope, handler InboxHandler) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= runtime.config.MaxAttempts; attempt++ {
		if _, err := runtime.inbox.Process(ctx, runtime.config.Name, envelope, handler); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < runtime.config.MaxAttempts {
			if err := waitForRetry(ctx, runtime.config.BaseBackoff<<min(attempt-1, 8)); err != nil {
				return err
			}
		}
	}
	if err := runtime.repository.Quarantine(ctx, runtime.config.Name, originalTopic, envelope, runtime.config.MaxAttempts, lastErr); err != nil {
		return errors.Join(lastErr, err)
	}
	return nil
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (repository *PostgresRepository) Quarantine(ctx context.Context, consumerName, originalTopic string, envelope Envelope, attempts int, cause error) error {
	if err := envelope.Validate(); err != nil {
		return err
	}
	message := strings.TrimSpace(cause.Error())
	if len(message) > 2000 {
		message = message[:2000]
	}
	transaction, err := repository.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin dead letter quarantine: %w", err)
	}
	defer transaction.Rollback()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode dead letter envelope: %w", err)
	}
	var deadLetterID string
	err = transaction.QueryRowContext(ctx, `
		INSERT INTO eventing.dead_letters (consumer_name, event_id, original_topic, envelope, attempts, last_error)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (consumer_name, event_id) DO NOTHING
		RETURNING id::text
	`, consumerName, envelope.ID, originalTopic, encoded, attempts, message).Scan(&deadLetterID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("persist dead letter: %w", err)
	}
	payload, err := json.Marshal(map[string]any{
		"dead_letter_id": deadLetterID, "consumer": consumerName, "attempts": attempts,
		"error": message, "original_envelope": envelope,
	})
	if err != nil {
		return fmt.Errorf("encode dead letter event: %w", err)
	}
	if _, err := repository.EnqueueTx(ctx, transaction, NewEvent{
		Topic: originalTopic + ".dlq", Type: "eventing.dead_lettered", Version: 1,
		AggregateType: "event", AggregateID: envelope.ID, AggregateVersion: int64(attempts),
		OccurredAt: time.Now().UTC(), TraceParent: envelope.TraceParent, Data: payload,
	}); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit dead letter quarantine: %w", err)
	}
	return nil
}
