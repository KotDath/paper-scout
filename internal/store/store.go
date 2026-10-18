// Package store owns the shared SQLite connection and embedded migrations.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	// DB serializes access through one connection. Close rows/transactions before
	// starting another operation; do not hold them across network requests.
	DB *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" || path == ":memory:" {
		return nil, errors.New("SQLite requires a persistent file path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := url.Values{}
	// DSN pragmas run on every connection, including replacements after errors.
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{DB: db}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate SQLite: %w", err)
	}
	if err := s.Check(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("check SQLite: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// Check verifies actual driver behavior, including a write and BM25 query on a
// temporary FTS5 table. Rollback removes the probe and leaves domain data alone.
func (s *Store) Check(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fk, timeout int
	var mode string
	if err := tx.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if fk != 1 || timeout != 5000 || mode != "wal" {
		return errors.New("required SQLite pragmas are not active")
	}
	if _, err := tx.ExecContext(ctx, "CREATE VIRTUAL TABLE temp.t01_fts_probe USING fts5(text)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO temp.t01_fts_probe(text) VALUES (?)", "agent memory consolidation"); err != nil {
		return err
	}
	var score float64
	if err := tx.QueryRowContext(ctx,
		"SELECT bm25(t01_fts_probe) FROM temp.t01_fts_probe WHERE t01_fts_probe MATCH ?", "memory").Scan(&score); err != nil {
		return err
	}
	if score >= 0 {
		return errors.New("FTS5 BM25 probe returned an unexpected score")
	}
	return nil
}
