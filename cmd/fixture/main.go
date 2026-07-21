package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/platform/fixture"
)

// main runs the fixture loader, reports any failure to standard error, and exits with status 1.
func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		os.Exit(1)
	}
}

// run loads the deterministic baseline fixture under a 20-second timeout and optionally loads a deterministic dataset, rejecting execution unless development fixtures are explicitly enabled and a database URL is set.
func run() error {
	if os.Getenv("SEA_ALLOW_DEVELOPMENT_FIXTURES") != "true" {
		return fmt.Errorf("refusing to load fixtures unless SEA_ALLOW_DEVELOPMENT_FIXTURES=true")
	}
	databaseURL := os.Getenv("SEA_DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("SEA_DATABASE_URL is required")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := fixture.ApplyBaseline(ctx, database); err != nil {
		return err
	}
	fmt.Println("loaded deterministic baseline fixture (seed 20260712, version 1)")
	if os.Getenv("SEA_LOAD_DATASET") == "true" {
		users, err := envInt("SEA_LOAD_DATASET_USERS", 1000)
		if err != nil {
			return err
		}
		videos, err := envInt("SEA_LOAD_DATASET_VIDEOS", 500)
		if err != nil {
			return err
		}
		stats, err := fixture.LoadDataset(ctx, database, 20260713, users, videos)
		if err != nil {
			return err
		}
		fmt.Printf("loaded deterministic load dataset: %+v\n", stats)
	}
	return nil
}

// envInt returns the positive integer stored in key, uses fallback when the variable is unset, and returns an error for invalid or non-positive values.
func envInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return value, nil
}
