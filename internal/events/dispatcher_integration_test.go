package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestDispatcherPublishesToRealKafkaBeforeMarkingOutboxDelivered(t *testing.T) {
	database := eventsTestDatabase(t)
	broker := os.Getenv("SEA_EVENTS_TEST_BROKER")
	if broker == "" {
		t.Skip("SEA_EVENTS_TEST_BROKER is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topic := fmt.Sprintf("sea-music-events-test-%d", time.Now().UnixNano())
	envelope := enqueueCommittedEvent(t, ctx, database, topic)
	publisher, err := events.NewKafkaPublisher([]string{broker})
	if err != nil {
		t.Fatalf("NewKafkaPublisher(): %v", err)
	}
	t.Cleanup(publisher.Close)
	dispatcher := events.NewDispatcher(events.NewPostgresRepository(database), publisher, "dispatcher-test", 10, time.Minute)
	count, err := dispatcher.RunOnce(ctx)
	if err != nil || count != 1 {
		t.Fatalf("RunOnce() = (%d, %v)", count, err)
	}
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(broker), kgo.ConsumeTopics(topic),
		kgo.ConsumerGroup(fmt.Sprintf("sea-music-events-consumer-%d", time.Now().UnixNano())),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("create Kafka consumer: %v", err)
	}
	defer consumer.Close()
	records := consumer.PollRecords(ctx, 1)
	if records.Err() != nil || len(records.Records()) != 1 {
		t.Fatalf("PollRecords() = %d records, %v", len(records.Records()), records.Err())
	}
	var delivered events.Envelope
	if err := json.Unmarshal(records.Records()[0].Value, &delivered); err != nil {
		t.Fatalf("decode Kafka envelope: %v", err)
	}
	if delivered.ID != envelope.ID || delivered.Type != envelope.Type {
		t.Fatalf("delivered envelope = %+v, want id %s type %s", delivered, envelope.ID, envelope.Type)
	}
	var state string
	var publishedAt *time.Time
	if err := database.QueryRowContext(ctx, `SELECT state, published_at FROM eventing.outbox WHERE id = $1`, envelope.ID).Scan(&state, &publishedAt); err != nil {
		t.Fatalf("read delivered outbox: %v", err)
	}
	if state != "published" || publishedAt == nil {
		t.Fatalf("outbox state = %q published_at=%v", state, publishedAt)
	}
}

func enqueueCommittedEvent(t *testing.T, ctx context.Context, database *sql.DB, topic string) events.Envelope {
	t.Helper()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx(): %v", err)
	}
	repository := events.NewPostgresRepository(database)
	envelope, err := repository.EnqueueTx(ctx, transaction, events.NewEvent{
		Topic: topic, Type: "video.published", Version: 1,
		AggregateType: "video", AggregateID: "01980c55-7c80-7abc-8def-0123456789ac", AggregateVersion: 4,
		OccurredAt: time.Now().UTC(), Data: json.RawMessage("{\"video_id\":\"01980c55-7c80-7abc-8def-0123456789ac\"}"),
	})
	if err != nil {
		_ = transaction.Rollback()
		t.Fatalf("EnqueueTx(): %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
	return envelope
}
