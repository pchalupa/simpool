// Package store owns simpool's SQLite persistence: schema migrations,
// transactional access and every query the pool layer needs. Leases and
// provenance are authoritative here; device existence is not.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pchalupa/simpool/internal/errs"

	"modernc.org/sqlite" // pure-Go driver: no C toolchain needed to install simpool
	sqlite3 "modernc.org/sqlite/lib"
)

// timeLayout is the storage format for every timestamp: UTC RFC3339 with
// nanoseconds, which sorts lexicographically.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

const (
	busyTimeout   = 10 * time.Second
	walRetryDelay = 10 * time.Millisecond
)

// Store is a handle on the pool database. A CLI invocation opens exactly one.
type Store struct {
	db   *sql.DB
	conn *sql.Conn
	path string
}

// Open prepares the state directory, opens the database and applies migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "creating state directory %q", filepath.Dir(path))
	}

	// journal_mode is not a DSN pragma: on a fresh file several processes
	// switching to WAL at once get SQLITE_BUSY without the busy handler
	// running, so enableWAL retries it after connecting.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)",
		path,
		busyTimeout.Milliseconds(),
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "opening pool database %q", path)
	}

	// Every invocation is short-lived and serialises its own writes, so a single
	// connection keeps `BEGIN IMMEDIATE` bound to the connection it runs on.
	db.SetMaxOpenConns(1)

	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()

		return nil, errs.Wrap(err, errs.CodeState, "connecting to pool database %q", path)
	}

	store := &Store{
		db:   db,
		conn: conn,
		path: path,
	}

	if err := store.enableWAL(ctx); err != nil {
		store.Close()

		return nil, err
	}

	if err := store.migrate(ctx); err != nil {
		store.Close()

		return nil, err
	}

	return store, nil
}

// Path returns the database file location.
func (s *Store) Path() string {
	return s.path
}

func (s *Store) enableWAL(ctx context.Context) error {
	deadline := time.Now().Add(busyTimeout)

	for {
		_, err := s.conn.ExecContext(ctx, "PRAGMA journal_mode=WAL")
		if err == nil {
			return nil
		}

		if !isBusy(err) || time.Now().After(deadline) {
			return errs.Wrap(err, errs.CodeState, "enabling WAL on pool database %q", s.path)
		}

		select {
		case <-ctx.Done():
			return errs.Wrap(ctx.Err(), errs.CodeState, "enabling WAL on pool database %q", s.path)
		case <-time.After(walRetryDelay):
		}
	}
}

func isBusy(err error) bool {
	var sqliteErr *sqlite.Error

	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
}

func (s *Store) Close() error {
	if s.conn != nil {
		s.conn.Close()
	}

	return s.db.Close()
}

// Tx is the query surface. The same methods serve read-only and write access;
// only Write wraps them in an immediate transaction.
type Tx struct {
	conn *sql.Conn
}

// Read runs fn outside a write transaction. Use it for reporting commands.
func (s *Store) Read(ctx context.Context, fn func(*Tx) error) error {
	return fn(&Tx{conn: s.conn})
}

// Write runs fn inside a `BEGIN IMMEDIATE` transaction, so two concurrent
// simpool processes serialise instead of racing on lease allocation.
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) error {
	if _, err := s.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return errs.Wrap(err, errs.CodeState, "starting write transaction")
	}

	tx := &Tx{conn: s.conn}

	if err := fn(tx); err != nil {
		if _, rollbackErr := s.conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
			return errors.Join(err, errs.Wrap(rollbackErr, errs.CodeState, "rolling back transaction"))
		}

		return err
	}

	if _, err := s.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return errs.Wrap(err, errs.CodeState, "committing transaction")
	}

	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(timeLayout)
}

func parseTime(raw string) (time.Time, error) {
	value, err := time.Parse(timeLayout, raw)
	if err != nil {
		return time.Time{}, errs.Wrap(err, errs.CodeState, "parsing stored timestamp %q", raw)
	}

	return value.UTC(), nil
}

func nullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}

	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}

	return &parsed, nil
}
