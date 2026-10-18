package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrate applies embedded, task-prefixed files in filename order. The ledger
// and all pending DDL commit together. Never edit an already applied migration.
func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bootstrap, err := migrations.ReadFile("migrations/T01_foundation.sql")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(bootstrap)); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	for _, name := range names {
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		var recorded string
		err = tx.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE name = ?", name).Scan(&recorded)
		switch err {
		case nil:
			if checksum != recorded {
				return fmt.Errorf("migration checksum mismatch: %s", name)
			}
			continue
		case sql.ErrNoRows:
		default:
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(name, checksum, applied_at) VALUES (?, ?, ?)",
			name, checksum, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
