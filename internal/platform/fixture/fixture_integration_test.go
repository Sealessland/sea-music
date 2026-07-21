package fixture_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/platform/fixture"
	"github.com/sealessland/sea-music/internal/platform/migrate"
)

// TestBaselineFixtureIsDeterministicAndIdempotent applies migrations and the baseline fixture twice, then verifies PostgreSQL retains exactly one manifest row with the fixed seed, version, and load timestamp; it skips when SEA_FIXTURE_TEST_DATABASE_URL is unset.
func TestBaselineFixtureIsDeterministicAndIdempotent(t *testing.T) {
	databaseURL := os.Getenv("SEA_FIXTURE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEA_FIXTURE_TEST_DATABASE_URL is required for the PostgreSQL integration test")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	migrations, err := migrate.Bundled()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := migrate.Apply(ctx, database, migrations); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	for range 2 {
		if err := fixture.ApplyBaseline(ctx, database); err != nil {
			t.Fatalf("ApplyBaseline() error = %v", err)
		}
	}
	var count int
	var seed int64
	var version int
	var loadedAt time.Time
	if err := database.QueryRowContext(ctx, `
		SELECT count(*), min(seed), min(fixture_version), min(loaded_at)
		FROM public.fixture_manifest
		WHERE fixture_name = 'baseline'
	`).Scan(&count, &seed, &version, &loadedAt); err != nil {
		t.Fatalf("query fixture manifest: %v", err)
	}
	if count != 1 || seed != 20260712 || version != 1 || !loadedAt.Equal(time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("fixture = count:%d seed:%d version:%d loaded:%s", count, seed, version, loadedAt)
	}
}

// TestLoadDatasetHasDeterministicRealisticDistribution loads the same seeded dataset twice after migration and verifies the resulting user, video, and interaction counts remain at the expected deterministic distribution; it skips when SEA_FIXTURE_TEST_DATABASE_URL is unset.
func TestLoadDatasetHasDeterministicRealisticDistribution(t *testing.T) {
	databaseURL := os.Getenv("SEA_FIXTURE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEA_FIXTURE_TEST_DATABASE_URL is required")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	migrations, _ := migrate.Bundled()
	if _, err := migrate.Apply(ctx, database, migrations); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	var first fixture.DatasetStats
	for range 2 {
		first, err = fixture.LoadDataset(ctx, database, 20260713, 40, 20)
		if err != nil {
			t.Fatalf("LoadDataset(): %v", err)
		}
	}
	if first.Users != 40 || first.Videos != 20 || first.Follows != 200 || first.Likes != 160 ||
		first.Favorites != 60 || first.Comments != 40 || first.Danmaku != 60 {
		t.Fatalf("dataset distribution = %+v", first)
	}
}
