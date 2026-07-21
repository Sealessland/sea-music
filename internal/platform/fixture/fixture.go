package fixture

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
)

//go:embed fixtures/*.sql
var files embed.FS

// ApplyBaseline executes the embedded baseline SQL in a transaction, rolling it back on execution or commit failure.
func ApplyBaseline(ctx context.Context, database *sql.DB) error {
	statement, err := files.ReadFile("fixtures/baseline.sql")
	if err != nil {
		return fmt.Errorf("read baseline fixture: %w", err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fixture transaction: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, string(statement)); err != nil {
		return fmt.Errorf("apply baseline fixture: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit baseline fixture: %w", err)
	}
	return nil
}
