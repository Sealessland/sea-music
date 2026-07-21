package events_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/events"
)

// TestInboxCommitsSideEffectOnceAndAcknowledgesDuplicate verifies that processing an envelope atomically commits its side effect and inbox claim once, while a duplicate is acknowledged without rerunning the handler.
func TestInboxCommitsSideEffectOnceAndAcknowledgesDuplicate(t *testing.T) {
	database := eventsTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	inbox := events.NewInbox(database)
	envelope := events.Envelope{
		ID: "01980c55-7c80-7abc-8def-0123456789ab", Type: "identity.user_registered", Version: 1,
		AggregateType: "user", AggregateID: "01980c55-7c80-7abc-8def-0123456789ac",
		OccurredAt: time.Now().UTC(), Data: []byte("{}"),
	}
	handler := func(ctx context.Context, transaction *sql.Tx, _ events.Envelope) error {
		_, err := transaction.ExecContext(ctx, `INSERT INTO identity.users (id, username, email, password_hash) VALUES ('01980c55-7c80-7abc-8def-0123456789ac', 'inbox_user', 'inbox@example.com', 'hash')`)
		return err
	}
	processed, err := inbox.Process(ctx, "identity-projection", envelope, handler)
	if err != nil || !processed {
		t.Fatalf("first Process() = (%v, %v)", processed, err)
	}
	processed, err = inbox.Process(ctx, "identity-projection", envelope, handler)
	if err != nil || processed {
		t.Fatalf("duplicate Process() = (%v, %v), want acknowledged duplicate", processed, err)
	}
	var users, inboxRows int
	if err := database.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM identity.users), (SELECT count(*) FROM eventing.inbox)`).Scan(&users, &inboxRows); err != nil {
		t.Fatalf("count inbox effects: %v", err)
	}
	if users != 1 || inboxRows != 1 {
		t.Fatalf("effects = users %d inbox %d, want 1/1", users, inboxRows)
	}
}

// TestInboxRollsBackClaimWhenSideEffectFails verifies that a handler error is returned with processed false and atomically rolls back both the side effect and inbox claim.
func TestInboxRollsBackClaimWhenSideEffectFails(t *testing.T) {
	database := eventsTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	inbox := events.NewInbox(database)
	envelope := events.Envelope{
		ID: "01980c55-7c80-7abc-8def-0123456789ad", Type: "identity.user_registered", Version: 1,
		AggregateType: "user", AggregateID: "01980c55-7c80-7abc-8def-0123456789ae",
		OccurredAt: time.Now().UTC(), Data: []byte("{}"),
	}
	expected := errors.New("side effect rejected")
	processed, err := inbox.Process(ctx, "identity-projection", envelope, func(ctx context.Context, transaction *sql.Tx, _ events.Envelope) error {
		_, insertErr := transaction.ExecContext(ctx, `INSERT INTO identity.users (id, username, email, password_hash) VALUES ('01980c55-7c80-7abc-8def-0123456789ae', 'rollback_user', 'rollback@example.com', 'hash')`)
		if insertErr != nil {
			return insertErr
		}
		return expected
	})
	if !errors.Is(err, expected) || processed {
		t.Fatalf("Process() = (%v, %v), want rolled-back error", processed, err)
	}
	var users, inboxRows int
	if err := database.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM identity.users), (SELECT count(*) FROM eventing.inbox)`).Scan(&users, &inboxRows); err != nil {
		t.Fatalf("count rollback effects: %v", err)
	}
	if users != 0 || inboxRows != 0 {
		t.Fatalf("rollback effects = users %d inbox %d", users, inboxRows)
	}
}
