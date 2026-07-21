package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	BrokerKafka    = "kafka"
	BrokerRocketMQ = "rocketmq"
)

// Publisher is the broker-independent boundary used by the Outbox dispatcher.
// Publish returns nil only after the selected broker durably acknowledges the event.
type Publisher interface {
	Publish(context.Context, OutboxEvent) error
	Ping(context.Context) error
	Close()
}

// Consumer is the broker-independent boundary used by Worker projection loops.
// RunOnce acknowledges a message only after Inbox processing or quarantine succeeds.
type Consumer interface {
	RunOnce(context.Context, InboxHandler) (bool, error)
	Close()
}

// BrokerConfig contains only transport-specific connection settings. Business
// event semantics live in ConsumerConfig and the Outbox/Inbox implementation.

type BrokerConfig struct {
	Driver       string
	Endpoints    []string
	AccessKey    string
	AccessSecret string
}

// NewPublisher selects one broker adapter. Adding a broker requires one new
// adapter file and one case here; callers remain broker-independent.
func NewPublisher(config BrokerConfig, topics ...string) (Publisher, error) {
	switch config.Driver {
	case BrokerKafka:
		return NewKafkaPublisher(config.Endpoints)
	case BrokerRocketMQ:
		endpoint, err := rocketMQEndpoint(config.Endpoints)
		if err != nil {
			return nil, err
		}
		return NewRocketMQPublisher(endpoint, config.AccessKey, config.AccessSecret, topics)
	default:
		return nil, fmt.Errorf("unsupported event broker %q", config.Driver)
	}
}

// NewConsumer selects one broker adapter while preserving the shared Inbox,
// retry, and DLQ behavior implemented by consumerRuntime.
func NewConsumer(config BrokerConfig, consumerConfig ConsumerConfig, inbox *Inbox, repository *PostgresRepository) (Consumer, error) {
	switch config.Driver {
	case BrokerKafka:
		return NewKafkaConsumer(config.Endpoints, consumerConfig, inbox, repository)
	case BrokerRocketMQ:
		endpoint, err := rocketMQEndpoint(config.Endpoints)
		if err != nil {
			return nil, err
		}
		return NewRocketMQConsumer(endpoint, config.AccessKey, config.AccessSecret, consumerConfig, inbox, repository)
	default:
		return nil, fmt.Errorf("unsupported event broker %q", config.Driver)
	}
}

func encodeEnvelope(envelope Envelope) ([]byte, error) {
	value, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode event envelope: %w", err)
	}
	return value, nil
}

func rocketMQEndpoint(endpoints []string) (string, error) {
	if len(endpoints) != 1 || strings.TrimSpace(endpoints[0]) == "" {
		return "", errors.New("RocketMQ requires exactly one proxy endpoint")
	}
	return strings.TrimSpace(endpoints[0]), nil
}
