package social

import (
	"context"
	"database/sql"
	"encoding/json"
)

type DomainEvent struct {
	Topic            string
	Type             string
	Version          int
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	Data             json.RawMessage
}

type OutboxWriter interface {
	Enqueue(context.Context, *sql.Tx, DomainEvent) (string, error)
}

type OutboxWriterFunc func(context.Context, *sql.Tx, DomainEvent) (string, error)

func (function OutboxWriterFunc) Enqueue(ctx context.Context, transaction *sql.Tx, event DomainEvent) (string, error) {
	return function(ctx, transaction, event)
}
