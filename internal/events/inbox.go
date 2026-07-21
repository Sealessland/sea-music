package events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type InboxHandler func(context.Context, *sql.Tx, Envelope) error

type Inbox struct {
	database *sql.DB
}

// NewInbox returns an inbox that persists event claims in database.
func NewInbox(database *sql.DB) *Inbox {
	return &Inbox{database: database}
}

// Process atomically claims an event for consumerName and invokes handler in the same transaction, returning true only when the claim and handler work are committed; duplicate claims return (false, nil).
func (inbox *Inbox) Process(ctx context.Context, consumerName string, envelope Envelope, handler InboxHandler) (bool, error) {
	consumerName = strings.TrimSpace(consumerName)
	if consumerName == "" || handler == nil {
		return false, errors.New("consumer name and handler are required")
	}
	if err := envelope.Validate(); err != nil {
		return false, err
	}
	transaction, err := inbox.database.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin inbox transaction: %w", err)
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `
		INSERT INTO eventing.inbox (consumer_name, event_id, event_type, event_version)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (consumer_name, event_id) DO NOTHING
	`, consumerName, envelope.ID, envelope.Type, envelope.Version)
	if err != nil {
		return false, fmt.Errorf("claim inbox event: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read inbox claim result: %w", err)
	}
	if rows == 0 {
		return false, nil
	}
	if err := handler(ctx, transaction, envelope); err != nil {
		return false, err
	}
	if err := transaction.Commit(); err != nil {
		return false, fmt.Errorf("commit inbox event: %w", err)
	}
	return true, nil
}
