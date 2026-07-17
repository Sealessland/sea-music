package events_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/events"
)

func TestVersionedEnvelopeRoundTripsStableDeliveryIdentity(t *testing.T) {
	occurredAt := time.Date(2026, 7, 13, 1, 2, 3, 0, time.UTC)
	envelope := events.Envelope{
		ID: "01980c55-7c80-7abc-8def-0123456789ab", Type: "video.published", Version: 1,
		AggregateType: "video", AggregateID: "01980c55-7c80-7abc-8def-0123456789ac",
		AggregateVersion: 4, OccurredAt: occurredAt,
		TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Data:        json.RawMessage(`{"title":"stable"}`),
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("Marshal(): %v", err)
	}
	var decoded events.Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal(): %v", err)
	}
	if decoded.ID != envelope.ID || decoded.AggregateVersion != 4 || decoded.OccurredAt != occurredAt || string(decoded.Data) != `{"title":"stable"}` {
		t.Fatalf("round trip changed envelope: %+v", decoded)
	}
}

func TestEnvelopeRejectsMissingVersionAndInvalidPayload(t *testing.T) {
	envelope := events.Envelope{ID: "event", Type: "video.published", Data: json.RawMessage(`{"broken"`), OccurredAt: time.Now()}
	if err := envelope.Validate(); err == nil {
		t.Fatal("Validate() error = nil")
	}
}
