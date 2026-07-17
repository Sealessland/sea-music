package discovery_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/discovery"
	"github.com/sealessland/sea-music/internal/platform/migrate"
)

func TestFollowingCursorRemainsStableWhenNewVideoArrivesBetweenPages(t *testing.T) {
	database := discoveryTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	viewerID, creatorID := insertDiscoveryUsers(t, ctx, database)
	if _, err := database.ExecContext(ctx, `INSERT INTO social.follows (follower_id, followee_id) VALUES ($1, $2)`, viewerID, creatorID); err != nil {
		t.Fatalf("insert follow: %v", err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	var oldestID string
	for index := range 3 {
		var id string
		if err := database.QueryRowContext(ctx, `
			INSERT INTO video.videos (creator_id, title, state, version, published_at)
			VALUES ($1, $2, 'published', 1, $3) RETURNING id::text
		`, creatorID, "followed video", base.Add(time.Duration(index)*time.Minute)).Scan(&id); err != nil {
			t.Fatalf("insert followed video: %v", err)
		}
		if index == 0 {
			oldestID = id
		}
	}
	repository := discovery.NewPostgresRepository(database)
	first, err := repository.Following(ctx, viewerID, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first Following() = (%+v, %v)", first, err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO video.videos (creator_id, title, state, version, published_at)
		VALUES ($1, 'new between pages', 'published', 1, now())
	`, creatorID); err != nil {
		t.Fatalf("insert between pages: %v", err)
	}
	second, err := repository.Following(ctx, viewerID, first.NextCursor, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != oldestID {
		t.Fatalf("second Following() = (%+v, %v)", second, err)
	}
}

func discoveryTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("SEA_DISCOVERY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SEA_DISCOVERY_TEST_DATABASE_URL is required")
	}
	database, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open discovery database: %v", err)
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
	if _, err := database.ExecContext(ctx, `TRUNCATE identity.users RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate discovery database: %v", err)
	}
	return database
}

func insertDiscoveryUsers(t *testing.T, ctx context.Context, database *sql.DB) (string, string) {
	t.Helper()
	var viewer, creator string
	if err := database.QueryRowContext(ctx, `INSERT INTO identity.users (username, email, password_hash) VALUES ('feed_viewer', 'viewer@example.com', 'hash') RETURNING id::text`).Scan(&viewer); err != nil {
		t.Fatalf("insert viewer: %v", err)
	}
	if err := database.QueryRowContext(ctx, `INSERT INTO identity.users (username, email, password_hash) VALUES ('feed_creator', 'creator@example.com', 'hash') RETURNING id::text`).Scan(&creator); err != nil {
		t.Fatalf("insert creator: %v", err)
	}
	return viewer, creator
}
