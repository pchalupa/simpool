package store

import (
	"context"
	"time"

	"github.com/pchalupa/simpool/internal/errs"
)

// migrations are applied in order; each entry is one schema version. Existing
// entries are immutable once released — add a new one instead.
var migrations = []string{
	`CREATE TABLE devices (
		id          TEXT PRIMARY KEY,
		platform    TEXT NOT NULL,
		name        TEXT NOT NULL,
		device_type TEXT NOT NULL,
		runtime     TEXT NOT NULL,
		state       TEXT NOT NULL,
		provenance  TEXT NOT NULL,
		borrowable  INTEGER NOT NULL DEFAULT 0,
		available   INTEGER NOT NULL DEFAULT 1,
		missing_at  TEXT,
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	);

	CREATE TABLE leases (
		id             TEXT PRIMARY KEY,
		device_id      TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
		borrower       TEXT NOT NULL,
		metadata       TEXT NOT NULL DEFAULT '{}',
		acquired_at    TEXT NOT NULL,
		expires_at     TEXT NOT NULL,
		released_at    TEXT,
		release_reason TEXT NOT NULL DEFAULT ''
	);

	-- One live lease per device, enforced by the database rather than by
	-- application-level checks, so concurrent processes cannot both win.
	CREATE UNIQUE INDEX idx_leases_live_device ON leases(device_id) WHERE released_at IS NULL;

	CREATE INDEX idx_leases_device ON leases(device_id);`,
}

// SchemaVersion is the schema version this build expects.
var SchemaVersion = len(migrations)

func (s *Store) migrate(ctx context.Context) error {
	const createVersions = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`

	if _, err := s.conn.ExecContext(ctx, createVersions); err != nil {
		return errs.Wrap(err, errs.CodeState, "creating migration table")
	}

	// Fast path: a warm database needs no write lock.
	applied, err := (&Tx{conn: s.conn}).AppliedSchemaVersion(ctx)
	if err != nil {
		return err
	}

	if err := checkSchemaVersion(applied); err != nil || applied == len(migrations) {
		return err
	}

	// Concurrent first runs all see a stale version above, so the version is
	// read again under the write lock and only the winner applies anything.
	return s.Write(ctx, func(tx *Tx) error {
		applied, err := tx.AppliedSchemaVersion(ctx)
		if err != nil {
			return err
		}

		if err := checkSchemaVersion(applied); err != nil {
			return err
		}

		for index := applied; index < len(migrations); index++ {
			if err := tx.applyMigration(ctx, index+1, migrations[index]); err != nil {
				return err
			}
		}

		return nil
	})
}

func checkSchemaVersion(applied int) error {
	if applied > len(migrations) {
		return errs.New(
			errs.CodeState,
			"pool database is at schema version %d but this simpool build understands %d; upgrade simpool",
			applied,
			len(migrations),
		)
	}

	return nil
}

func (t *Tx) applyMigration(ctx context.Context, version int, statement string) error {
	if _, err := t.conn.ExecContext(ctx, statement); err != nil {
		return errs.Wrap(err, errs.CodeState, "applying migration %d", version)
	}

	const record = `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`

	if _, err := t.conn.ExecContext(ctx, record, version, formatTime(time.Now())); err != nil {
		return errs.Wrap(err, errs.CodeState, "recording migration %d", version)
	}

	return nil
}

// AppliedSchemaVersion reports the schema version stored in the database.
func (t *Tx) AppliedSchemaVersion(ctx context.Context) (int, error) {
	var version int

	err := t.conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return 0, errs.Wrap(err, errs.CodeState, "reading schema version")
	}

	return version, nil
}
