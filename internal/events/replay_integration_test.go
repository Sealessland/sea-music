package events_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/events"
)

func TestReplayOutboxEventResetsFailedEventForRedispatch(t *testing.T) {
	database := eventsTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	envelope := enqueueCommittedEvent(t, ctx, database, "domain-events")
	// The outbox replay path never touches Kafka, so no publisher is needed.
	service := events.NewReplayService(events.NewPostgresRepository(database), nil)
	if _, err := database.ExecContext(ctx, `
		UPDATE eventing.outbox
		SET state = 'failed', attempts = max_attempts, lease_owner = 'crashed-dispatcher',
		    lease_until = now() + interval '1 minute', available_at = now() + interval '1 hour',
		    last_error = 'broker unreachable'
		WHERE id = $1
	`, envelope.ID); err != nil {
		t.Fatalf("force outbox failure: %v", err)
	}
	if err := service.ReplayOutboxEvent(ctx, envelope.ID, "admin"); err != nil {
		t.Fatalf("ReplayOutboxEvent(): %v", err)
	}
	var state string
	var attempts int
	var leaseOwner, lastError sql.NullString
	var leaseUntil sql.NullTime
	if err := database.QueryRowContext(ctx, `
		SELECT state, attempts, lease_owner, lease_until, last_error
		FROM eventing.outbox WHERE id = $1
	`, envelope.ID).Scan(&state, &attempts, &leaseOwner, &leaseUntil, &lastError); err != nil {
		t.Fatalf("read replayed outbox: %v", err)
	}
	if state != "pending" || attempts != 0 || leaseOwner.Valid || leaseUntil.Valid || lastError.Valid {
		t.Fatalf("replayed outbox = state %q attempts %d lease_owner %v lease_until %v last_error %v",
			state, attempts, leaseOwner, leaseUntil, lastError)
	}
	claimed, err := events.NewPostgresRepository(database).ClaimBatch(ctx, "replay-dispatcher", 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].Envelope.ID != envelope.ID {
		t.Fatalf("ClaimBatch() after replay = (%d, %v), want the replayed event", len(claimed), err)
	}
}

func TestReplayOutboxEventRejectsNonFailedOrMissingEvents(t *testing.T) {
	database := eventsTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	envelope := enqueueCommittedEvent(t, ctx, database, "domain-events")
	service := events.NewReplayService(events.NewPostgresRepository(database), nil)
	if err := service.ReplayOutboxEvent(ctx, envelope.ID, "viewer"); !errors.Is(err, events.ErrReplayForbidden) {
		t.Fatalf("non-admin replay error = %v, want ErrReplayForbidden", err)
	}
	if err := service.ReplayOutboxEvent(ctx, envelope.ID, "admin"); !errors.Is(err, events.ErrOutboxEventNotFailed) {
		t.Fatalf("pending event replay error = %v, want ErrOutboxEventNotFailed", err)
	}
	if err := service.ReplayOutboxEvent(ctx, "01980c55-7c80-7abc-8def-0123456789ff", "admin"); !errors.Is(err, events.ErrOutboxEventNotFound) {
		t.Fatalf("missing event replay error = %v, want ErrOutboxEventNotFound", err)
	}
}
