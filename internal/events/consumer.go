package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
)

type ConsumerConfig struct {
	Brokers     []string
	Topic       string
	Group       string
	Name        string
	MaxAttempts int
	BaseBackoff time.Duration
}

type KafkaConsumer struct {
	config     ConsumerConfig
	client     *kgo.Client
	inbox      *Inbox
	repository *PostgresRepository
}

func NewKafkaConsumer(config ConsumerConfig, inbox *Inbox, repository *PostgresRepository) (*KafkaConsumer, error) {
	if len(config.Brokers) == 0 || strings.TrimSpace(config.Topic) == "" || strings.TrimSpace(config.Group) == "" ||
		strings.TrimSpace(config.Name) == "" || config.MaxAttempts <= 0 || config.BaseBackoff <= 0 || inbox == nil || repository == nil {
		return nil, errors.New("invalid Kafka consumer configuration")
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(config.Brokers...),
		kgo.ConsumeTopics(config.Topic),
		kgo.ConsumerGroup(config.Group),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.WithHooks(kotel.NewKotel(kotel.WithTracer(kotel.NewTracer(kotel.ConsumerGroup(config.Group)))).Hooks()...),
	)
	if err != nil {
		return nil, fmt.Errorf("create Kafka consumer: %w", err)
	}
	return &KafkaConsumer{config: config, client: client, inbox: inbox, repository: repository}, nil
}

func (consumer *KafkaConsumer) RunOnce(ctx context.Context, handler InboxHandler) (bool, error) {
	records := consumer.client.PollRecords(ctx, 1)
	if err := records.Err(); err != nil {
		return false, err
	}
	all := records.Records()
	if len(all) == 0 {
		return false, nil
	}
	record := all[0]
	processContext := record.Context
	if processContext == nil {
		processContext = ctx
	}
	var envelope Envelope
	if err := json.Unmarshal(record.Value, &envelope); err != nil {
		return false, fmt.Errorf("decode consumed envelope: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return false, err
	}
	var lastErr error
	for attempt := 1; attempt <= consumer.config.MaxAttempts; attempt++ {
		_, err := consumer.inbox.Process(processContext, consumer.config.Name, envelope, handler)
		if err == nil {
			if err := consumer.client.CommitRecords(ctx, record); err != nil {
				return false, fmt.Errorf("commit Kafka record: %w", err)
			}
			return true, nil
		}
		lastErr = err
		if attempt < consumer.config.MaxAttempts {
			timer := time.NewTimer(consumer.config.BaseBackoff << min(attempt-1, 8))
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if err := consumer.repository.Quarantine(ctx, consumer.config.Name, record.Topic, envelope, consumer.config.MaxAttempts, lastErr); err != nil {
		return false, errors.Join(lastErr, err)
	}
	if err := consumer.client.CommitRecords(ctx, record); err != nil {
		return false, fmt.Errorf("commit quarantined Kafka record: %w", err)
	}
	return true, nil
}

func (consumer *KafkaConsumer) Close() {
	consumer.client.Close()
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
