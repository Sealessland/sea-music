package events_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/events"
)

func TestAckWindowCrashRepublishesStableIDAndInboxDeduplicates(t *testing.T) {
	database := eventsTestDatabase(t)
	broker := os.Getenv("SEA_EVENTS_TEST_BROKER")
	if broker == "" {
		t.Skip("SEA_EVENTS_TEST_BROKER is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topic := fmt.Sprintf("sea-music-recovery-%d", time.Now().UnixNano())
	envelope := enqueueCommittedEvent(t, ctx, database, topic)
	repository := events.NewPostgresRepository(database)
	publisher, err := events.NewKafkaPublisher([]string{broker})
	if err != nil {
		t.Fatalf("NewKafkaPublisher(): %v", err)
	}
	defer publisher.Close()
	claimed, err := repository.ClaimBatch(ctx, "crashed-dispatcher", 10, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimBatch() = (%d, %v)", len(claimed), err)
	}
	if err := publisher.Publish(ctx, claimed[0]); err != nil {
		t.Fatalf("publish before crash: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE eventing.outbox SET lease_until = now() - interval '1 second' WHERE id = $1`, envelope.ID); err != nil {
		t.Fatalf("expire dispatcher lease: %v", err)
	}
	dispatcher := events.NewDispatcher(repository, publisher, "recovery-dispatcher", 10, time.Minute)
	if count, err := dispatcher.RunOnce(ctx); err != nil || count != 1 {
		t.Fatalf("recovery dispatch = (%d, %v)", count, err)
	}
	stats, err := repository.Backlog(ctx)
	if err != nil || stats.Pending != 0 || stats.Publishing != 0 {
		t.Fatalf("recovered backlog = (%+v, %v)", stats, err)
	}
	consumer, err := events.NewKafkaConsumer(events.ConsumerConfig{
		Brokers: []string{broker}, Topic: topic, Group: topic + "-group", Name: "dedupe-projection",
		MaxAttempts: 2, BaseBackoff: time.Millisecond,
	}, events.NewInbox(database), repository)
	if err != nil {
		t.Fatalf("NewKafkaConsumer(): %v", err)
	}
	defer consumer.Close()
	handlerCalls := 0
	handler := func(ctx context.Context, transaction *sql.Tx, _ events.Envelope) error {
		handlerCalls++
		_, err := transaction.ExecContext(ctx, `INSERT INTO identity.users (username, email, password_hash) VALUES ('dedupe_user', 'dedupe@example.com', 'hash')`)
		return err
	}
	for range 2 {
		if processed, err := consumer.RunOnce(ctx, handler); err != nil || !processed {
			t.Fatalf("consume duplicate = (%v, %v)", processed, err)
		}
	}
	if handlerCalls != 1 {
		t.Fatalf("duplicate handler calls = %d, want 1", handlerCalls)
	}
	var inboxRows int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM eventing.inbox WHERE event_id = $1`, envelope.ID).Scan(&inboxRows); err != nil {
		t.Fatalf("count dedupe inbox: %v", err)
	}
	if inboxRows != 1 {
		t.Fatalf("dedupe inbox rows = %d, want 1", inboxRows)
	}
}
