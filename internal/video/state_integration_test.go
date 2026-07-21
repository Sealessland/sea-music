package video_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/platform/migrate"
	"github.com/sealessland/sea-music/internal/video"
)

// TestPostgresTransitionUsesCompareAndSwapAndWritesAudit verifies that a successful PostgreSQL state transition increments the version and writes one audit record, while a stale expected version returns ErrVersionConflict without adding another record.
func TestPostgresTransitionUsesCompareAndSwapAndWritesAudit(t *testing.T) {
	database := videoTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var creatorID string
	if err := database.QueryRowContext(ctx, `
		INSERT INTO identity.users (username, email, password_hash)
		VALUES ('video_creator', 'video@example.com', '$argon2id$fixture')
		RETURNING id::text
	`).Scan(&creatorID); err != nil {
		t.Fatalf("create video test user: %v", err)
	}
	repository := video.NewPostgresRepository(database)
	draft, err := repository.CreateDraft(ctx, creatorID, "State machine", "integration test")
	if err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}
	uploaded, err := repository.Transition(ctx, draft.ID, creatorID, 0, video.StateUploaded, "source accepted")
	if err != nil || uploaded.Version != 1 || uploaded.State != video.StateUploaded {
		t.Fatalf("Transition() = (%+v, %v)", uploaded, err)
	}
	if _, err := repository.Transition(ctx, draft.ID, creatorID, 0, video.StateWithdrawn, "stale writer"); !errors.Is(err, video.ErrVersionConflict) {
		t.Fatalf("stale Transition() error = %v, want ErrVersionConflict", err)
	}
	var auditCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM video.state_transitions WHERE video_id = $1`, draft.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count state transitions: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count = %d, want 1", auditCount)
	}
}

// videoTestDatabase opens the PostgreSQL integration-test database, skipping when SEA_VIDEO_TEST_DATABASE_URL is unset, applies bundled migrations, truncates related tables, and registers cleanup for the connection and timeout context.
func videoTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("SEA_VIDEO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEA_VIDEO_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
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
	if _, err := database.ExecContext(ctx, `TRUNCATE video.state_transitions, video.processing_jobs, video.renditions, video.source_assets, video.videos, identity.sessions, identity.users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate video test tables: %v", err)
	}
	return database
}
