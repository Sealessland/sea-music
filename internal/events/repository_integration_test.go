package events_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/events"
	"github.com/sealessland/sea-music/internal/platform/migrate"
)

func TestBusinessWriteAndOutboxEventRollbackAtomically(t *testing.T) {
	database := eventsTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repository := events.NewPostgresRepository(database)
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx(): %v", err)
	}
	var userID string
	if err := transaction.QueryRowContext(ctx, `INSERT INTO identity.users (username, email, password_hash) VALUES ('atomic_user', 'atomic@example.com', 'hash') RETURNING id::text`).Scan(&userID); err != nil {
		t.Fatalf("insert business row: %v", err)
	}
	envelope, err := repository.EnqueueTx(ctx, transaction, events.NewEvent{
		Topic: "domain-events", Type: "identity.user_registered", Version: 1,
		AggregateType: "user", AggregateID: userID, AggregateVersion: 0,
		OccurredAt: time.Now().UTC(), TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		Data: json.RawMessage("{\"username\":\"atomic_user\"}"),
	})
	if err != nil || envelope.ID == "" {
		t.Fatalf("EnqueueTx() = (%+v, %v)", envelope, err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
	var users, outbox int
	if err := database.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM identity.users), (SELECT count(*) FROM eventing.outbox)`).Scan(&users, &outbox); err != nil {
		t.Fatalf("count rolled back rows: %v", err)
	}
	if users != 0 || outbox != 0 {
		t.Fatalf("rollback left users=%d outbox=%d", users, outbox)
	}
}

func eventsTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("SEA_EVENTS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEA_EVENTS_TEST_DATABASE_URL is required")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open events database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	migrations, err := migrate.Bundled()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := migrate.Apply(ctx, database, migrations); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	if _, err := database.ExecContext(ctx, `TRUNCATE eventing.dead_letters, eventing.inbox, eventing.outbox, video.state_transitions, video.processing_jobs, video.renditions, video.source_assets, video.videos, identity.sessions, identity.users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate events test tables: %v", err)
	}
	return database
}
