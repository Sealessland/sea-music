package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
)

type OutboxEvent struct {
	Topic       string
	Envelope    Envelope
	Attempts    int
	MaxAttempts int
}

type BacklogStats struct {
	Pending       int64
	Publishing    int64
	Failed        int64
	OldestSeconds float64
}

// Backlog returns counts by active outbox state and the age in seconds of the oldest pending or publishing event.
func (repository *PostgresRepository) Backlog(ctx context.Context) (BacklogStats, error) {
	var stats BacklogStats
	err := repository.database.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE state = 'pending'),
		       count(*) FILTER (WHERE state = 'publishing'),
		       count(*) FILTER (WHERE state = 'failed'),
		       COALESCE(EXTRACT(EPOCH FROM (now() - min(occurred_at) FILTER (WHERE state IN ('pending', 'publishing')))), 0)
		FROM eventing.outbox
	`).Scan(&stats.Pending, &stats.Publishing, &stats.Failed, &stats.OldestSeconds)
	if err != nil {
		return BacklogStats{}, fmt.Errorf("read outbox backlog: %w", err)
	}
	return stats, nil
}

// ClaimBatch atomically leases up to limit eligible outbox events to dispatcherID, including expired leases, and increments each event's attempt count.
func (repository *PostgresRepository) ClaimBatch(ctx context.Context, dispatcherID string, limit int, leaseDuration time.Duration) ([]OutboxEvent, error) {
	if strings.TrimSpace(dispatcherID) == "" || limit <= 0 || limit > 1000 || leaseDuration <= 0 {
		return nil, errors.New("invalid outbox claim request")
	}
	now := time.Now().UTC()
	rows, err := repository.database.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM eventing.outbox
			WHERE attempts < max_attempts
			  AND ((state = 'pending' AND available_at <= $1) OR (state = 'publishing' AND lease_until < $1))
			ORDER BY available_at, occurred_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE eventing.outbox o
		SET state = 'publishing', attempts = attempts + 1, lease_owner = $3,
		    lease_until = $4, updated_at = $1
		FROM candidates
		WHERE o.id = candidates.id
		RETURNING o.topic, o.id::text, o.event_type, o.event_version, o.aggregate_type,
		          o.aggregate_id::text, o.aggregate_version, o.occurred_at,
		          COALESCE(o.traceparent, ''), o.payload, o.attempts, o.max_attempts
	`, now, limit, dispatcherID, now.Add(leaseDuration))
	if err != nil {
		return nil, fmt.Errorf("claim outbox batch: %w", err)
	}
	defer rows.Close()
	claimed := make([]OutboxEvent, 0, limit)
	for rows.Next() {
		var event OutboxEvent
		var payload []byte
		if err := rows.Scan(
			&event.Topic, &event.Envelope.ID, &event.Envelope.Type, &event.Envelope.Version,
			&event.Envelope.AggregateType, &event.Envelope.AggregateID, &event.Envelope.AggregateVersion,
			&event.Envelope.OccurredAt, &event.Envelope.TraceParent, &payload,
			&event.Attempts, &event.MaxAttempts,
		); err != nil {
			return nil, fmt.Errorf("scan claimed outbox event: %w", err)
		}
		event.Envelope.Data = json.RawMessage(payload)
		if err := event.Envelope.Validate(); err != nil {
			return nil, fmt.Errorf("claimed invalid outbox event %s: %w", event.Envelope.ID, err)
		}
		claimed = append(claimed, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate outbox batch: %w", err)
	}
	return claimed, nil
}

// MarkPublished marks a publishing event as published and clears its lease and last error, returning an error if dispatcherID no longer owns the lease.
func (repository *PostgresRepository) MarkPublished(ctx context.Context, eventID, dispatcherID string) error {
	now := time.Now().UTC()
	result, err := repository.database.ExecContext(ctx, `
		UPDATE eventing.outbox
		SET state = 'published', published_at = $3, lease_owner = NULL, lease_until = NULL,
		    last_error = NULL, updated_at = $3
		WHERE id = $1 AND state = 'publishing' AND lease_owner = $2
	`, eventID, dispatcherID, now)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read outbox publish result: %w", err)
	}
	if rows != 1 {
		return errors.New("outbox delivery lease lost")
	}
	return nil
}

// FailDelivery records a truncated delivery error, clears the owned lease, and either schedules the event after backoff or marks it failed when attempts are exhausted.
func (repository *PostgresRepository) FailDelivery(ctx context.Context, event OutboxEvent, dispatcherID string, cause error, backoff time.Duration) error {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 2000 {
		message = message[:2000]
	}
	state := "pending"
	if event.Attempts >= event.MaxAttempts {
		state = "failed"
	}
	now := time.Now().UTC()
	result, err := repository.database.ExecContext(ctx, `
		UPDATE eventing.outbox
		SET state = $3, available_at = $4, lease_owner = NULL, lease_until = NULL,
		    last_error = $5, updated_at = $6
		WHERE id = $1 AND state = 'publishing' AND lease_owner = $2
	`, event.Envelope.ID, dispatcherID, state, now.Add(backoff), message, now)
	if err != nil {
		return fmt.Errorf("record outbox delivery failure: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read outbox failure result: %w", err)
	}
	if rows != 1 {
		return errors.New("outbox delivery lease lost")
	}
	return nil
}

// Publisher delivers an outbox event and reports broker reachability. Successful Publish calls are durable broker acknowledgements.
type Publisher interface {
	Publish(context.Context, OutboxEvent) error
	Ping(context.Context) error
	Close()
}

type KafkaPublisher struct {
	client *kgo.Client
}

// NewKafkaPublisher creates a traced Kafka producer for the supplied brokers with automatic topic creation and unlimited unknown-topic retries.
func NewKafkaPublisher(brokers []string) (*KafkaPublisher, error) {
	if len(brokers) == 0 {
		return nil, errors.New("at least one Kafka broker is required")
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.AllowAutoTopicCreation(),
		kgo.UnknownTopicRetries(-1),
		kgo.WithHooks(kotel.NewKotel(kotel.WithTracer(kotel.NewTracer())).Hooks()...),
	)
	if err != nil {
		return nil, fmt.Errorf("create Kafka producer: %w", err)
	}
	return &KafkaPublisher{client: client}, nil
}

func encodeEnvelope(envelope Envelope) ([]byte, error) {
	value, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode event envelope: %w", err)
	}
	return value, nil
}

func (publisher *KafkaPublisher) Publish(ctx context.Context, event OutboxEvent) error {
	value, err := encodeEnvelope(event.Envelope)
	if err != nil {
		return err
	}
	headers := []kgo.RecordHeader{
		{Key: "event_id", Value: []byte(event.Envelope.ID)},
		{Key: "event_type", Value: []byte(event.Envelope.Type)},
		{Key: "traceparent", Value: []byte(event.Envelope.TraceParent)},
	}
	record := &kgo.Record{Topic: event.Topic, Key: []byte(event.Envelope.AggregateID), Value: value, Headers: headers, Timestamp: event.Envelope.OccurredAt}
	if err := publisher.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		return fmt.Errorf("Kafka publish acknowledgement: %w", err)
	}
	return nil
}

// Ping checks connectivity to the configured Kafka cluster within ctx.
func (publisher *KafkaPublisher) Ping(ctx context.Context) error {
	return publisher.client.Ping(ctx)
}

// Close shuts down the Kafka client and releases its resources.
func (publisher *KafkaPublisher) Close() {
	publisher.client.Close()
}

type Dispatcher struct {
	repository    *PostgresRepository
	publisher     Publisher
	dispatcherID  string
	batchSize     int
	leaseDuration time.Duration
}

// NewDispatcher creates a dispatcher that claims batches under dispatcherID using the specified batch size and lease duration.
func NewDispatcher(repository *PostgresRepository, publisher Publisher, dispatcherID string, batchSize int, leaseDuration time.Duration) *Dispatcher {
	return &Dispatcher{repository: repository, publisher: publisher, dispatcherID: dispatcherID, batchSize: batchSize, leaseDuration: leaseDuration}
}

// RunOnce claims and publishes one batch sequentially, stopping at the first error and returning the number marked published; publish failures are rescheduled with capped exponential backoff or marked failed after the final attempt.
func (dispatcher *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	batch, err := dispatcher.repository.ClaimBatch(ctx, dispatcher.dispatcherID, dispatcher.batchSize, dispatcher.leaseDuration)
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, event := range batch {
		if err := dispatcher.publisher.Publish(ctx, event); err != nil {
			backoff := time.Second << min(event.Attempts-1, 8)
			failureCtx := ctx
			cancel := func() {}
			if ctx.Err() != nil {
				failureCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			}
			if failErr := dispatcher.repository.FailDelivery(failureCtx, event, dispatcher.dispatcherID, err, backoff); failErr != nil {
				cancel()
				return delivered, errors.Join(err, failErr)
			}
			cancel()
			return delivered, err
		}
		if err := dispatcher.repository.MarkPublished(ctx, event.Envelope.ID, dispatcher.dispatcherID); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}
