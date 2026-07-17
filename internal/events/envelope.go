package events

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

var traceParentPattern = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

type Envelope struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Version          int             `json:"version"`
	AggregateType    string          `json:"aggregate_type"`
	AggregateID      string          `json:"aggregate_id"`
	AggregateVersion int64           `json:"aggregate_version"`
	OccurredAt       time.Time       `json:"occurred_at"`
	TraceParent      string          `json:"traceparent,omitempty"`
	Data             json.RawMessage `json:"data"`
}

func (envelope Envelope) Validate() error {
	if strings.TrimSpace(envelope.ID) == "" || strings.TrimSpace(envelope.Type) == "" {
		return errors.New("event id and type are required")
	}
	if envelope.Version <= 0 {
		return errors.New("event version must be positive")
	}
	if strings.TrimSpace(envelope.AggregateType) == "" || strings.TrimSpace(envelope.AggregateID) == "" || envelope.AggregateVersion < 0 {
		return errors.New("valid aggregate identity and version are required")
	}
	if envelope.OccurredAt.IsZero() {
		return errors.New("event occurrence time is required")
	}
	if envelope.TraceParent != "" && !traceParentPattern.MatchString(envelope.TraceParent) {
		return errors.New("traceparent is invalid")
	}
	if len(envelope.Data) == 0 || !json.Valid(envelope.Data) {
		return errors.New("event data must be valid JSON")
	}
	return nil
}
