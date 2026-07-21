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

func TestNewPublisherRejectsUnsupportedBroker(t *testing.T) {
	t.Parallel()

	_, err := NewPublisher(BrokerConfig{Driver: "unknown", Endpoints: []string{"broker:1"}}, "domain-events")
	if err == nil || !strings.Contains(err.Error(), "unsupported event broker") {
		t.Fatalf("NewPublisher() error = %v, want unsupported event broker", err)
	}
}
