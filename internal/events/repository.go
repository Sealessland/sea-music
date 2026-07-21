package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type NewEvent struct {
	Topic            string
	Type             string
	Version          int
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	OccurredAt       time.Time
	TraceParent      string
	Data             json.RawMessage
}

type PostgresRepository struct {
	database *sql.DB
}

// NewPostgresRepository returns a repository that uses database for PostgreSQL-backed event storage.
func NewPostgresRepository(database *sql.DB) *PostgresRepository {
	return &PostgresRepository{database: database}
}

// EnqueueTx validates event, inserts it into the outbox within transaction, and returns its database-assigned envelope; it rejects a nil transaction or blank topic and wraps insert failures.
func (repository *PostgresRepository) EnqueueTx(ctx context.Context, transaction *sql.Tx, event NewEvent) (Envelope, error) {
	if transaction == nil || strings.TrimSpace(event.Topic) == "" {
		return Envelope{}, errors.New("transaction and event topic are required")
	}
	candidate := Envelope{
		ID: "pending", Type: event.Type, Version: event.Version,
		AggregateType: event.AggregateType, AggregateID: event.AggregateID,
		AggregateVersion: event.AggregateVersion, OccurredAt: event.OccurredAt,
		TraceParent: event.TraceParent, Data: event.Data,
	}
	if err := candidate.Validate(); err != nil {
		return Envelope{}, err
	}
	err := transaction.QueryRowContext(ctx, `
		INSERT INTO eventing.outbox (
			topic, event_type, event_version, aggregate_type, aggregate_id,
			aggregate_version, payload, traceparent, occurred_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9)
		RETURNING id::text
	`, event.Topic, event.Type, event.Version, event.AggregateType, event.AggregateID,
		event.AggregateVersion, event.Data, event.TraceParent, event.OccurredAt,
	).Scan(&candidate.ID)
	if err != nil {
		return Envelope{}, fmt.Errorf("enqueue outbox event: %w", err)
	}
	return candidate, nil
}
