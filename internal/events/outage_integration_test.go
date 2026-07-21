package events_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/events"
)

// TestOutboxBacklogSurvivesBrokerOutageAndDrainsAfterRecovery verifies that a committed event remains pending with one recorded attempt after publication fails, then is published exactly once when made available after broker recovery; it skips unless SEA_EVENTS_TEST_BROKER is set.
func TestOutboxBacklogSurvivesBrokerOutageAndDrainsAfterRecovery(t *testing.T) {
	database := eventsTestDatabase(t)
	broker := os.Getenv("SEA_EVENTS_TEST_BROKER")
	if broker == "" {
		t.Skip("SEA_EVENTS_TEST_BROKER is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	envelope := enqueueCommittedEvent(t, ctx, database, "sea-music-outage-recovery")
	unavailablePublisher, err := events.NewKafkaPublisher([]string{"127.0.0.1:1"})
	if err != nil {
		t.Fatalf("create unavailable publisher: %v", err)
	}
	failureCtx, failureCancel := context.WithTimeout(ctx, 250*time.Millisecond)
	_, publishErr := events.NewDispatcher(events.NewPostgresRepository(database), unavailablePublisher, "outage-dispatcher", 10, time.Minute).RunOnce(failureCtx)
	failureCancel()
	unavailablePublisher.Close()
	if publishErr == nil {
		t.Fatal("broker outage unexpectedly published event")
	}
	var state string
	var attempts int
	if err := database.QueryRowContext(ctx, `SELECT state, attempts FROM eventing.outbox WHERE id = $1`, envelope.ID).Scan(&state, &attempts); err != nil {
		t.Fatalf("read outage backlog: %v", err)
	}
	if state != "pending" || attempts != 1 {
		t.Fatalf("outage backlog = state %q attempts %d", state, attempts)
	}
	if _, err := database.ExecContext(ctx, `UPDATE eventing.outbox SET available_at = now() WHERE id = $1`, envelope.ID); err != nil {
		t.Fatalf("make outage event available: %v", err)
	}
	recoveredPublisher, err := events.NewKafkaPublisher([]string{broker})
	if err != nil {
		t.Fatalf("create recovered publisher: %v", err)
	}
	defer recoveredPublisher.Close()
	count, err := events.NewDispatcher(events.NewPostgresRepository(database), recoveredPublisher, "recovered-dispatcher", 10, time.Minute).RunOnce(ctx)
	if err != nil || count != 1 {
		t.Fatalf("recovered dispatch = (%d, %v)", count, err)
	}
}
