package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

var (
	_ Publisher = (*JetStreamPublisher)(nil)
	_ Consumer  = (*JetStreamConsumer)(nil)
)

const jetStreamFetchWait = time.Second

var jetStreamNamePart = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func jetStreamName(topic string) string {
	return "SEA_MUSIC_" + strings.ToUpper(jetStreamNamePart.ReplaceAllString(topic, "_"))
}

func connectJetStream(endpoint, name string) (*nats.Conn, jetstream.JetStream, error) {
	connection, err := nats.Connect(endpoint, nats.Name(name), nats.Timeout(5*time.Second))
	if err != nil {
		return nil, nil, fmt.Errorf("connect NATS: %w", err)
	}
	js, err := jetstream.New(connection)
	if err != nil {
		connection.Close()
		return nil, nil, fmt.Errorf("create JetStream context: %w", err)
	}
	return connection, js, nil
}

func ensureJetStream(ctx context.Context, js jetstream.JetStream, topic string) (jetstream.Stream, error) {
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       jetStreamName(topic),
		Subjects:   []string{topic, topic + ".dlq"},
		Storage:    jetstream.FileStorage,
		Retention:  jetstream.LimitsPolicy,
		Duplicates: 2 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure JetStream stream for %s: %w", topic, err)
	}
	return stream, nil
}

// JetStreamPublisher maps the broker-independent Publisher contract to
// acknowledged, file-backed JetStream publications.
type JetStreamPublisher struct {
	connection *nats.Conn
	js         jetstream.JetStream
}

func NewJetStreamPublisher(ctx context.Context, endpoint string, topics []string) (*JetStreamPublisher, error) {
	if strings.TrimSpace(endpoint) == "" || len(topics) == 0 {
		return nil, errors.New("JetStream endpoint and at least one topic are required")
	}
	connection, js, err := connectJetStream(endpoint, "sea-music-publisher")
	if err != nil {
		return nil, err
	}
	publisher := &JetStreamPublisher{connection: connection, js: js}
	for _, topic := range topics {
		if strings.TrimSpace(topic) == "" {
			publisher.Close()
			return nil, errors.New("JetStream topics must not be empty")
		}
		if _, err := ensureJetStream(ctx, js, topic); err != nil {
			publisher.Close()
			return nil, err
		}
	}
	return publisher, nil
}

func (publisher *JetStreamPublisher) Publish(ctx context.Context, event OutboxEvent) error {
	value, err := encodeEnvelope(event.Envelope)
	if err != nil {
		return err
	}
	message := &nats.Msg{Subject: event.Topic, Data: value, Header: nats.Header{}}
	message.Header.Set("event_id", event.Envelope.ID)
	message.Header.Set("event_type", event.Envelope.Type)
	message.Header.Set("traceparent", event.Envelope.TraceParent)
	message.Header.Set("aggregate_id", event.Envelope.AggregateID)
	if _, err := publisher.js.PublishMsg(ctx, message, jetstream.WithMsgID(event.Envelope.ID)); err != nil {
		return fmt.Errorf("JetStream publish acknowledgement: %w", err)
	}
	return nil
}

func (publisher *JetStreamPublisher) Ping(ctx context.Context) error {
	_, err := publisher.js.AccountInfo(ctx)
	return err
}

func (publisher *JetStreamPublisher) Close() {
	publisher.js.CleanupPublisher()
	publisher.connection.Close()
}

// JetStreamConsumer maps a durable pull consumer and explicit double ACKs to
// the shared Consumer contract.
type JetStreamConsumer struct {
	connection *nats.Conn
	consumer   jetstream.Consumer
	runtime    consumerRuntime
}

func NewJetStreamConsumer(ctx context.Context, endpoint string, config ConsumerConfig, inbox *Inbox, repository *PostgresRepository) (*JetStreamConsumer, error) {
	runtime, err := newConsumerRuntime(config, inbox, repository)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("JetStream endpoint is required")
	}
	connection, js, err := connectJetStream(endpoint, "sea-music-"+config.Group)
	if err != nil {
		return nil, err
	}
	stream, err := ensureJetStream(ctx, js, config.Topic)
	if err != nil {
		connection.Close()
		return nil, err
	}
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       config.Group,
		FilterSubject: config.Topic,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		connection.Close()
		return nil, fmt.Errorf("ensure JetStream consumer %s: %w", config.Group, err)
	}
	return &JetStreamConsumer{connection: connection, consumer: consumer, runtime: runtime}, nil
}

func (consumer *JetStreamConsumer) RunOnce(ctx context.Context, handler InboxHandler) (bool, error) {
	message, err := consumer.consumer.Next(jetstream.FetchMaxWait(jetStreamFetchWait))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if errors.Is(err, nats.ErrTimeout) || errors.Is(err, jetstream.ErrNoMessages) {
			return false, nil
		}
		return false, err
	}
	var envelope Envelope
	if err := json.Unmarshal(message.Data(), &envelope); err != nil {
		return false, fmt.Errorf("decode consumed envelope: %w", err)
	}
	if err := consumer.runtime.process(ctx, consumer.runtime.config.Topic, envelope, handler); err != nil {
		return false, err
	}
	if err := message.DoubleAck(ctx); err != nil {
		return false, fmt.Errorf("acknowledge JetStream message: %w", err)
	}
	return true, nil
}

func (consumer *JetStreamConsumer) Close() {
	consumer.connection.Close()
}
