package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sealessland/sea-music/internal/platform/migrate"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "migrate: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := os.Getenv("SEA_DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("SEA_DATABASE_URL is required")
	}
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	command := "up"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	switch command {
	case "up":
		migrations, err := migrate.Bundled()
		if err != nil {
			return err
		}
		count, err := migrate.Apply(ctx, database, migrations)
		if err != nil {
			return err
		}
		fmt.Printf("applied %d migration(s)\n", count)
		return nil
	case "status":
		status, err := migrate.Status(ctx, database)
		if err != nil {
			return err
		}
		for _, item := range status {
			fmt.Printf("%s %s %s\n", item.Version, item.Name, item.Checksum[:12])
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q; use up or status", command)
	}
}
