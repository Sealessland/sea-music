package events_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sealessland/sea-music/internal/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

// TestPoisonEventRetriesThenReachesRealDeadLetterTopic verifies that a failing Kafka event is retried three times, leaves no inbox row, persists one dead letter, dispatches that dead letter to the <topic>.dlq topic, rejects replay by a non-admin, and after admin replay marks the dead letter as replayed with replay_count 1.
func TestPoisonEventRetriesThenReachesRealDeadLetterTopic(t *testing.T) {
	database := eventsTestDatabase(t)
	broker := os.Getenv("SEA_EVENTS_TEST_BROKER")
	if broker == "" {
		t.Skip("SEA_EVENTS_TEST_BROKER is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topic := fmt.Sprintf("sea-music-poison-%d", time.Now().UnixNano())
	publisher, err := events.NewKafkaPublisher([]string{broker})
	if err != nil {
		t.Fatalf("NewKafkaPublisher(): %v", err)
	}
	defer publisher.Close()
	consumer, err := events.NewKafkaConsumer(events.ConsumerConfig{
		Brokers: []string{broker}, Topic: topic, Group: topic + "-group",
		Name: "poison-projection", MaxAttempts: 3, BaseBackoff: time.Millisecond,
	}, events.NewInbox(database), events.NewPostgresRepository(database))
	if err != nil {
		t.Fatalf("NewKafkaConsumer(): %v", err)
	}
	defer consumer.Close()
	envelope := events.Envelope{
		ID: "01980c55-7c80-7abc-8def-0123456789af", Type: "video.poison", Version: 1,
		AggregateType: "video", AggregateID: "01980c55-7c80-7abc-8def-0123456789ac",
		OccurredAt: time.Now().UTC(), Data: []byte("{}"),
	}
	if err := publisher.Publish(ctx, events.OutboxEvent{Topic: topic, Envelope: envelope}); err != nil {
		t.Fatalf("publish poison event: %v", err)
	}
	handlerCalls := 0
	processed, err := consumer.RunOnce(ctx, func(context.Context, *sql.Tx, events.Envelope) error {
		handlerCalls++
		return errors.New("unsupported payload")
	})
	if err != nil || !processed || handlerCalls != 3 {
		t.Fatalf("RunOnce() = (%v, %v), handler calls %d", processed, err, handlerCalls)
	}
	var deadLetters, inboxRows int
	if err := database.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM eventing.dead_letters), (SELECT count(*) FROM eventing.inbox)`).Scan(&deadLetters, &inboxRows); err != nil {
		t.Fatalf("count poison results: %v", err)
	}
	if deadLetters != 1 || inboxRows != 0 {
		t.Fatalf("poison results = dead letters %d inbox %d", deadLetters, inboxRows)
	}
	dispatcher := events.NewDispatcher(events.NewPostgresRepository(database), publisher, "dlq-dispatcher", 10, time.Minute)
	if count, err := dispatcher.RunOnce(ctx); err != nil || count != 1 {
		t.Fatalf("dispatch dead letter = (%d, %v)", count, err)
	}
	dlqConsumer, err := kgo.NewClient(
		kgo.SeedBrokers(broker), kgo.ConsumeTopics(topic+".dlq"),
		kgo.ConsumerGroup(topic+"-dlq-reader"), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("create DLQ consumer: %v", err)
	}
	defer dlqConsumer.Close()
	records := dlqConsumer.PollRecords(ctx, 1)
	if records.Err() != nil || len(records.Records()) != 1 {
		t.Fatalf("read DLQ = %d records, %v", len(records.Records()), records.Err())
	}
	var deadLetterID string
	if err := database.QueryRowContext(ctx, `SELECT id::text FROM eventing.dead_letters WHERE event_id = $1`, envelope.ID).Scan(&deadLetterID); err != nil {
		t.Fatalf("read dead letter id: %v", err)
	}
	replay := events.NewReplayService(events.NewPostgresRepository(database), publisher)
	if err := replay.Replay(ctx, deadLetterID, "member"); !errors.Is(err, events.ErrReplayForbidden) {
		t.Fatalf("member Replay() error = %v, want ErrReplayForbidden", err)
	}
	if err := replay.Replay(ctx, deadLetterID, "admin"); err != nil {
		t.Fatalf("admin Replay(): %v", err)
	}
	var status string
	var replayCount int
	if err := database.QueryRowContext(ctx, `SELECT status, replay_count FROM eventing.dead_letters WHERE id = $1`, deadLetterID).Scan(&status, &replayCount); err != nil {
		t.Fatalf("read replayed dead letter: %v", err)
	}
	if status != "replayed" || replayCount != 1 {
		t.Fatalf("replay state = %q count %d", status, replayCount)
	}
}
