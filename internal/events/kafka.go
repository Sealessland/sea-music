package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
)

var (
	_ Publisher = (*KafkaPublisher)(nil)
	_ Consumer  = (*KafkaConsumer)(nil)
)

// KafkaPublisher maps the broker-independent Publisher contract to Kafka records.
type KafkaPublisher struct {
	client *kgo.Client
}

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

func (publisher *KafkaPublisher) Publish(ctx context.Context, event OutboxEvent) error {
	value, err := encodeEnvelope(event.Envelope)
	if err != nil {
		return err
	}
	record := &kgo.Record{
		Topic: event.Topic,
		Key:   []byte(event.Envelope.AggregateID),
		Value: value,
		Headers: []kgo.RecordHeader{
			{Key: "event_id", Value: []byte(event.Envelope.ID)},
			{Key: "event_type", Value: []byte(event.Envelope.Type)},
			{Key: "traceparent", Value: []byte(event.Envelope.TraceParent)},
		},
		Timestamp: event.Envelope.OccurredAt,
	}
	if err := publisher.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		return fmt.Errorf("Kafka publish acknowledgement: %w", err)
	}
	return nil
}

func (publisher *KafkaPublisher) Ping(ctx context.Context) error {
	return publisher.client.Ping(ctx)
}

func (publisher *KafkaPublisher) Close() {
	publisher.client.Close()
}

// KafkaConsumer maps Kafka group offsets to the broker-independent Consumer contract.
type KafkaConsumer struct {
	client  *kgo.Client
	runtime consumerRuntime
}

func NewKafkaConsumer(brokers []string, config ConsumerConfig, inbox *Inbox, repository *PostgresRepository) (*KafkaConsumer, error) {
	runtime, err := newConsumerRuntime(config, inbox, repository)
	if err != nil {
		return nil, err
	}
	if len(brokers) == 0 {
		return nil, errors.New("at least one Kafka broker is required")
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(config.Topic),
		kgo.ConsumerGroup(config.Group),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.WithHooks(kotel.NewKotel(kotel.WithTracer(kotel.NewTracer(kotel.ConsumerGroup(config.Group)))).Hooks()...),
	)
	if err != nil {
		return nil, fmt.Errorf("create Kafka consumer: %w", err)
	}
	return &KafkaConsumer{client: client, runtime: runtime}, nil
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
	if err := consumer.runtime.process(processContext, record.Topic, envelope, handler); err != nil {
		return false, err
	}
	if err := consumer.client.CommitRecords(ctx, record); err != nil {
		return false, fmt.Errorf("commit Kafka record: %w", err)
	}
	return true, nil
}

func (consumer *KafkaConsumer) Close() {
	consumer.client.Close()
}
