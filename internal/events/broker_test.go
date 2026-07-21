package events

import (
	"strings"
	"testing"
)

func TestRocketMQEndpointRequiresExactlyOneProxy(t *testing.T) {
	t.Parallel()

	for _, endpoints := range [][]string{nil, {}, {""}, {"proxy-a:8081", "proxy-b:8081"}} {
		if _, err := rocketMQEndpoint(endpoints); err == nil {
			t.Fatalf("rocketMQEndpoint(%q) succeeded, want validation error", endpoints)
		}
	}

	endpoint, err := rocketMQEndpoint([]string{" proxy:8081 "})
	if err != nil {
		t.Fatalf("rocketMQEndpoint() error = %v", err)
	}
	if endpoint != "proxy:8081" {
		t.Fatalf("rocketMQEndpoint() = %q, want proxy:8081", endpoint)
	}
}

func TestJetStreamRequiresExactlyOneServerURL(t *testing.T) {
	t.Parallel()

	for _, endpoints := range [][]string{nil, {}, {""}, {"nats://a:4222", "nats://b:4222"}} {
		if _, err := jetStreamEndpoint(endpoints); err == nil {
			t.Fatalf("jetStreamEndpoint(%q) succeeded, want validation error", endpoints)
		}
	}

	endpoint, err := jetStreamEndpoint([]string{" nats://127.0.0.1:4222 "})
	if err != nil {
		t.Fatalf("jetStreamEndpoint() error = %v", err)
	}
	if endpoint != "nats://127.0.0.1:4222" {
		t.Fatalf("jetStreamEndpoint() = %q", endpoint)
	}
}

func TestNewPublisherRejectsUnsupportedBroker(t *testing.T) {
	t.Parallel()

	_, err := NewPublisher(BrokerConfig{Driver: "unknown", Endpoints: []string{"broker:1"}}, "domain-events")
	if err == nil || !strings.Contains(err.Error(), "unsupported event broker") {
		t.Fatalf("NewPublisher() error = %v, want unsupported event broker", err)
	}
}

func TestNewConsumerRejectsUnsupportedBrokerBeforeDependencies(t *testing.T) {
	t.Parallel()

	_, err := NewConsumer(BrokerConfig{Driver: "unknown"}, ConsumerConfig{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported event broker") {
		t.Fatalf("NewConsumer() error = %v, want unsupported event broker", err)
	}
}

func TestRocketMQTopicTranslatesUnsupportedCharacters(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"domain-events":     "domain-events",
		"domain-events.dlq": "domain-events_dlq",
	} {
		if got := rocketMQTopic(input); got != want {
			t.Errorf("rocketMQTopic(%q) = %q, want %q", input, got, want)
		}
	}
}
