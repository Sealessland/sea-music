package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	rmq "github.com/apache/rocketmq-clients/golang/v5"
	"github.com/apache/rocketmq-clients/golang/v5/credentials"
)

var (
	_ Publisher = (*RocketMQPublisher)(nil)
	_ Consumer  = (*RocketMQConsumer)(nil)
)

const (
	rocketMQFetchWait         = 30 * time.Second
	rocketMQInvisibleDuration = 30 * time.Second
)

var rocketMQTopicPart = strings.NewReplacer(".", "_")

func rocketMQTopic(topic string) string {
	return rocketMQTopicPart.Replace(topic)
}

type rocketMQMessage interface {
	Body() []byte
}

type rocketMQSimpleConsumer interface {
	Receive(context.Context, int32, time.Duration) ([]rocketMQMessage, error)
	Ack(context.Context, rocketMQMessage) error
	Close() error
}

type rocketMQMessageView struct {
	view *rmq.MessageView
}

func (message rocketMQMessageView) Body() []byte {
	return message.view.GetBody()
}

type rocketMQConsumerClient struct {
	consumer rmq.SimpleConsumer
}

func (client *rocketMQConsumerClient) Receive(ctx context.Context, count int32, invisible time.Duration) ([]rocketMQMessage, error) {
	views, err := client.consumer.Receive(ctx, count, invisible)
	if err != nil {
		return nil, err
	}
	messages := make([]rocketMQMessage, len(views))
	for index, view := range views {
		messages[index] = rocketMQMessageView{view: view}
	}
	return messages, nil
}

func (client *rocketMQConsumerClient) Ack(ctx context.Context, message rocketMQMessage) error {
	view, ok := message.(rocketMQMessageView)
	if !ok || view.view == nil {
		return errors.New("invalid RocketMQ message acknowledgement")
	}
	return client.consumer.Ack(ctx, view.view)
}

func (client *rocketMQConsumerClient) Close() error {
	return client.consumer.GracefulStop()
}

func rocketMQConfig(endpoint, accessKey, accessSecret, group string) *rmq.Config {
	return &rmq.Config{
		Endpoint:      endpoint,
		ConsumerGroup: group,
		Credentials:   &credentials.SessionCredentials{AccessKey: accessKey, AccessSecret: accessSecret},
	}
}

func newRocketMQSimpleConsumer(endpoint, accessKey, accessSecret, topic, group string) (rocketMQSimpleConsumer, error) {
	rmq.EnableSsl = false
	consumer, err := rmq.NewSimpleConsumer(
		rocketMQConfig(endpoint, accessKey, accessSecret, group),
		rmq.WithSimpleAwaitDuration(rocketMQFetchWait),
		rmq.WithSimpleSubscriptionExpressions(map[string]*rmq.FilterExpression{topic: rmq.SUB_ALL}),
	)
	if err != nil {
		return nil, fmt.Errorf("create RocketMQ consumer: %w", err)
	}
	if err := consumer.Start(); err != nil {
		return nil, fmt.Errorf("start RocketMQ consumer: %w", err)
	}
	return &rocketMQConsumerClient{consumer: consumer}, nil
}

// RocketMQPublisher maps the broker-independent Publisher contract to RocketMQ messages.
type RocketMQPublisher struct {
	endpoint string
	producer rmq.Producer
}

func NewRocketMQPublisher(endpoint, accessKey, accessSecret string, topics []string) (*RocketMQPublisher, error) {
	if strings.TrimSpace(endpoint) == "" || len(topics) == 0 {
		return nil, errors.New("RocketMQ endpoint and at least one topic are required")
	}
	rmq.EnableSsl = false
	mappedTopics := make([]string, len(topics))
	for index, topic := range topics {
		mappedTopics[index] = rocketMQTopic(topic)
	}
	producer, err := rmq.NewProducer(rocketMQConfig(endpoint, accessKey, accessSecret, ""), rmq.WithTopics(mappedTopics...))
	if err != nil {
		return nil, fmt.Errorf("create RocketMQ producer: %w", err)
	}
	if err := producer.Start(); err != nil {
		return nil, fmt.Errorf("start RocketMQ producer: %w", err)
	}
	return &RocketMQPublisher{endpoint: endpoint, producer: producer}, nil
}

func (publisher *RocketMQPublisher) Publish(ctx context.Context, event OutboxEvent) error {
	value, err := encodeEnvelope(event.Envelope)
	if err != nil {
		return err
	}
	message := &rmq.Message{Topic: rocketMQTopic(event.Topic), Body: value}
	message.SetKeys(event.Envelope.ID, event.Envelope.AggregateID)
	message.AddProperty("event_type", event.Envelope.Type)
	message.AddProperty("traceparent", event.Envelope.TraceParent)
	if _, err := publisher.producer.Send(ctx, message); err != nil {
		return fmt.Errorf("RocketMQ publish acknowledgement: %w", err)
	}
	return nil
}

func (publisher *RocketMQPublisher) Ping(ctx context.Context) error {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", publisher.endpoint)
	if err != nil {
		return fmt.Errorf("dial RocketMQ proxy: %w", err)
	}
	return connection.Close()
}

func (publisher *RocketMQPublisher) Close() {
	_ = publisher.producer.GracefulStop()
}

// RocketMQConsumer maps RocketMQ invisibility acknowledgements to the shared
// Consumer contract. An acknowledgement follows Inbox processing or DLQ write.
type RocketMQConsumer struct {
	client  rocketMQSimpleConsumer
	runtime consumerRuntime
}

func NewRocketMQConsumer(endpoint, accessKey, accessSecret string, config ConsumerConfig, inbox *Inbox, repository *PostgresRepository) (*RocketMQConsumer, error) {
	runtime, err := newConsumerRuntime(config, inbox, repository)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("RocketMQ endpoint is required")
	}
	client, err := newRocketMQSimpleConsumer(endpoint, accessKey, accessSecret, rocketMQTopic(config.Topic), config.Group)
	if err != nil {
		return nil, err
	}
	return &RocketMQConsumer{client: client, runtime: runtime}, nil
}

func (consumer *RocketMQConsumer) RunOnce(ctx context.Context, handler InboxHandler) (bool, error) {
	messages, err := consumer.client.Receive(ctx, 1, rocketMQInvisibleDuration)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, err
	}
	if len(messages) == 0 {
		return false, nil
	}
	message := messages[0]
	var envelope Envelope
	if err := json.Unmarshal(message.Body(), &envelope); err != nil {
		return false, fmt.Errorf("decode consumed envelope: %w", err)
	}
	if err := consumer.runtime.process(ctx, consumer.runtime.config.Topic, envelope, handler); err != nil {
		return false, err
	}
	if err := consumer.client.Ack(ctx, message); err != nil {
		return false, fmt.Errorf("acknowledge RocketMQ message: %w", err)
	}
	return true, nil
}

func (consumer *RocketMQConsumer) Close() {
	_ = consumer.client.Close()
}
