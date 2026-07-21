package events

import (
	"errors"
	"fmt"
	"strings"
)

const (
	BrokerKafka    = "kafka"
	BrokerRocketMQ = "rocketmq"
)

type BrokerConfig struct {
	Driver       string
	Endpoints    []string
	AccessKey    string
	AccessSecret string
}

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

func NewConsumer(config BrokerConfig, consumerConfig ConsumerConfig, inbox *Inbox, repository *PostgresRepository) (Consumer, error) {
	switch config.Driver {
	case BrokerKafka:
		consumerConfig.Brokers = config.Endpoints
		return NewKafkaConsumer(consumerConfig, inbox, repository)
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

func rocketMQEndpoint(endpoints []string) (string, error) {
	if len(endpoints) != 1 || strings.TrimSpace(endpoints[0]) == "" {
		return "", errors.New("RocketMQ requires exactly one proxy endpoint")
	}
	return strings.TrimSpace(endpoints[0]), nil
}
