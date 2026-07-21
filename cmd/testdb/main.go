package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var safeTestDatabase = regexp.MustCompile(`^[a-z][a-z0-9_]*_test$`)

// main resets the test database and reports any failure to standard error before exiting with status 1.
func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "testdb: %v\n", err)
		os.Exit(1)
	}
}

// run validates the admin URL and safe test database name, then within a 20-second timeout terminates existing connections, drops and recreates the database, and reports success to standard output.
func run() error {
	adminURL := os.Getenv("SEA_DATABASE_ADMIN_URL")
	if adminURL == "" {
		return fmt.Errorf("SEA_DATABASE_ADMIN_URL is required")
	}
	target := os.Getenv("SEA_TEST_DATABASE_NAME")
	if target == "" {
		target = "sea_music_test"
	}
	if !safeTestDatabase.MatchString(target) {
		return fmt.Errorf("refusing to reset database %q: name must end in _test", target)
	}

	database, err := sql.Open("pgx", adminURL)
	if err != nil {
		return fmt.Errorf("open admin database: %w", err)
	}
	defer database.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := database.ExecContext(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`,
		target,
	); err != nil {
		return fmt.Errorf("terminate test database connections: %w", err)
	}
	quoted := `"` + target + `"`
	if _, err := database.ExecContext(ctx, `DROP DATABASE IF EXISTS `+quoted); err != nil {
		return fmt.Errorf("drop test database: %w", err)
	}
	if _, err := database.ExecContext(ctx, `CREATE DATABASE `+quoted); err != nil {
		return fmt.Errorf("create test database: %w", err)
	}
	fmt.Printf("reset test database %s\n", target)
	return nil
}
