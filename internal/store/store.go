// Package store is the SQLite persistence layer.
//
// One process owns the database file (see README §9.2: exactly one replica),
// and the pool is limited to a single connection. For a single-user app that
// costs nothing and removes SQLITE_BUSY and lock-upgrade deadlocks entirely.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	"modernc.org/sqlite"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/migrations"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("already exists")
	ErrInvalid   = errors.New("invalid")
	ErrAmbiguous = errors.New("ambiguous")
	ErrNotEmpty  = errors.New("database is not empty")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and migrates it to the
// latest schema before returning.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	q := url.Values{}
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "synchronous(NORMAL)", "busy_timeout(5000)"} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping is for readiness checks.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// tx runs fn in a transaction, committing only if it returns nil.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// mapErr turns SQLite constraint failures into the package's sentinel errors.
func mapErr(err error, what string) error {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	switch se.Code() {
	case 2067, 1555: // SQLITE_CONSTRAINT_UNIQUE, SQLITE_CONSTRAINT_PRIMARYKEY
		return fmt.Errorf("%s: %w", what, ErrConflict)
	case 787: // SQLITE_CONSTRAINT_FOREIGNKEY
		return fmt.Errorf("%s: referenced row does not exist: %w", what, ErrInvalid)
	case 275: // SQLITE_CONSTRAINT_CHECK
		return fmt.Errorf("%s: %w: %v", what, ErrInvalid, err)
	}
	return err
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- nullable column helpers -------------------------------------------------

func nullDate(d *civil.Date) any {
	if d == nil {
		return nil
	}
	return d.String()
}

func nullTime(t *civil.Time) any {
	if t == nil {
		return nil
	}
	return t.String()
}

func nullString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func scanDate(ns sql.NullString) (*civil.Date, error) {
	if !ns.Valid {
		return nil, nil
	}
	d, err := civil.ParseDate(ns.String)
	return &d, err
}

func scanTime(ns sql.NullString) (*civil.Time, error) {
	if !ns.Valid {
		return nil, nil
	}
	t, err := civil.ParseTime(ns.String)
	return &t, err
}

func scanInt(ni sql.NullInt64) *int64 {
	if !ni.Valid {
		return nil
	}
	return &ni.Int64
}

// placeholders returns "?,?,?" for n values.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Backup writes a consistent, compacted copy of the database to path while the
// app keeps running (VACUUM INTO). An existing file at path is replaced.
func (s *Store) Backup(ctx context.Context, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
