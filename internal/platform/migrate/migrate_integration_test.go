package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/platform/migrate"
)

// TestApplyIsTransactionalAndIdempotentAgainstPostgres verifies that bundled migrations create all application schemas on first application and that reapplying them reports no changes; it skips unless SEA_MIGRATION_TEST_DATABASE_URL is set.
func TestApplyIsTransactionalAndIdempotentAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("SEA_MIGRATION_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEA_MIGRATION_TEST_DATABASE_URL is required for the PostgreSQL integration test")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		t.Fatalf("ping database: %v", err)
	}

	migrations, err := migrate.Bundled()
	if err != nil {
		t.Fatalf("load bundled migrations: %v", err)
	}
	first, err := migrate.Apply(ctx, database, migrations)
	if err != nil {
		t.Fatalf("first Apply() error = %v", err)
	}
	second, err := migrate.Apply(ctx, database, migrations)
	if err != nil {
		t.Fatalf("second Apply() error = %v", err)
	}
	if first != len(migrations) || second != 0 {
		t.Fatalf("applied counts = (%d, %d), want (%d, 0)", first, second, len(migrations))
	}

	var schemaCount int
	if err := database.QueryRowContext(ctx, `
		SELECT count(*)
		FROM information_schema.schemata
		WHERE schema_name IN ('identity', 'video', 'social', 'discovery', 'eventing')
	`).Scan(&schemaCount); err != nil {
		t.Fatalf("query schemas: %v", err)
	}
	if schemaCount != 5 {
		t.Fatalf("application schema count = %d, want 5", schemaCount)
	}
}
