package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"time"
)

var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

//go:embed migrations/*.sql
var bundled embed.FS

type Migration struct {
	Version  string
	Name     string
	SQL      string
	Checksum string
}

type Applied struct {
	Version   string
	Name      string
	Checksum  string
	AppliedAt time.Time
}

func Bundled() ([]Migration, error) {
	root, err := fs.Sub(bundled, "migrations")
	if err != nil {
		return nil, fmt.Errorf("open bundled migrations: %w", err)
	}
	return Load(root)
}

func Load(files fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	seen := make(map[string]string, len(entries))
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid migration filename %q; want NNNN_description.sql", entry.Name())
		}
		if previous, exists := seen[match[1]]; exists {
			return nil, fmt.Errorf("duplicate migration version %s in %q and %q", match[1], previous, entry.Name())
		}
		data, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(data)
		migrations = append(migrations, Migration{
			Version:  match[1],
			Name:     match[2],
			SQL:      string(data),
			Checksum: hex.EncodeToString(digest[:]),
		})
		seen[match[1]] = entry.Name()
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

func Apply(ctx context.Context, database *sql.DB, migrations []Migration) (int, error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Close()

	if _, err := connection.ExecContext(ctx, `SELECT pg_advisory_lock(736241903)`); err != nil {
		return 0, fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = connection.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(736241903)`)
	}()

	if _, err := connection.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version text PRIMARY KEY,
			name text NOT NULL,
			checksum text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return 0, fmt.Errorf("ensure migration table: %w", err)
	}

	applied := 0
	for _, migration := range migrations {
		var checksum string
		err := connection.QueryRowContext(ctx,
			`SELECT checksum FROM schema_migrations WHERE version = $1`, migration.Version,
		).Scan(&checksum)
		switch {
		case err == nil:
			if checksum != migration.Checksum {
				return applied, fmt.Errorf("migration %s checksum mismatch", migration.Version)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return applied, fmt.Errorf("read migration %s status: %w", migration.Version, err)
		}

		transaction, err := connection.BeginTx(ctx, nil)
		if err != nil {
			return applied, fmt.Errorf("begin migration %s: %w", migration.Version, err)
		}
		if _, err := transaction.ExecContext(ctx, migration.SQL); err != nil {
			_ = transaction.Rollback()
			return applied, fmt.Errorf("execute migration %s: %w", migration.Version, err)
		}
		if _, err := transaction.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`,
			migration.Version, migration.Name, migration.Checksum,
		); err != nil {
			_ = transaction.Rollback()
			return applied, fmt.Errorf("record migration %s: %w", migration.Version, err)
		}
		if err := transaction.Commit(); err != nil {
			return applied, fmt.Errorf("commit migration %s: %w", migration.Version, err)
		}
		applied++
	}
	return applied, nil
}

func Status(ctx context.Context, database *sql.DB) ([]Applied, error) {
	rows, err := database.QueryContext(ctx, `
		SELECT version, name, checksum, applied_at
		FROM schema_migrations
		ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("query migration status: %w", err)
	}
	defer rows.Close()

	var result []Applied
	for rows.Next() {
		var item Applied
		if err := rows.Scan(&item.Version, &item.Name, &item.Checksum, &item.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan migration status: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration status: %w", err)
	}
	return result, nil
}
