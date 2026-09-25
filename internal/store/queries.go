package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/strv/simpool/internal/errs"
)

const deviceColumns = `id, platform, name, device_type, runtime, state, provenance,
	borrowable, available, missing_at, created_at, updated_at`

const leaseColumns = `id, device_id, borrower, metadata,
	acquired_at, expires_at, released_at, release_reason`

// metroPortPath is the JSON path the live-lease uniqueness index is built on.
const metroPortPath = `json_extract(metadata, '$.metro_port')`

// Observation is what the provider currently reports about a device.
type Observation struct {
	ID         string
	Platform   string
	Name       string
	DeviceType string
	Runtime    string
	State      string
	Available  bool
	// Provenance and Borrowable apply only when the device is new to the pool.
	Provenance string
	Borrowable bool
}

// ObserveDevice records what the platform reports. Provenance and borrowability
// are set only on first sight: they are simpool's own bookkeeping and must not
// be overwritten by a later observation.
func (t *Tx) ObserveDevice(ctx context.Context, observation Observation, now time.Time) error {
	const statement = `INSERT INTO devices (
		id, platform, name, device_type, runtime, state, provenance,
		borrowable, available, missing_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		name        = excluded.name,
		device_type = excluded.device_type,
		runtime     = excluded.runtime,
		state       = excluded.state,
		available   = excluded.available,
		missing_at  = NULL,
		updated_at  = excluded.updated_at`

	_, err := t.conn.ExecContext(
		ctx,
		statement,
		observation.ID,
		observation.Platform,
		observation.Name,
		observation.DeviceType,
		observation.Runtime,
		observation.State,
		observation.Provenance,
		observation.Borrowable,
		observation.Available,
		formatTime(now),
		formatTime(now),
	)
	if err != nil {
		return errs.Wrap(err, errs.CodeState, "recording device %q", observation.ID)
	}

	return nil
}

// MarkDeviceMissing flags devices the platform no longer reports. It returns
// the identifiers that changed from present to missing.
func (t *Tx) MarkDeviceMissing(ctx context.Context, presentIDs []string, now time.Time) ([]string, error) {
	query := `SELECT id FROM devices WHERE missing_at IS NULL`
	args := []any{}

	if len(presentIDs) > 0 {
		query += " AND id NOT IN (" + placeholders(len(presentIDs)) + ")"

		for _, id := range presentIDs {
			args = append(args, id)
		}
	}

	rows, err := t.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "listing present devices")
	}

	missing := []string{}

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()

			return nil, errs.Wrap(err, errs.CodeState, "scanning device id")
		}

		missing = append(missing, id)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "listing present devices")
	}

	for _, id := range missing {
		const update = `UPDATE devices SET missing_at = ?, state = ?, updated_at = ? WHERE id = ?`

		if _, err := t.conn.ExecContext(ctx, update, formatTime(now), "unknown", formatTime(now), id); err != nil {
			return nil, errs.Wrap(err, errs.CodeState, "marking device %q missing", id)
		}
	}

	return missing, nil
}

// GetDevice returns one device, or a device_not_found error.
func (t *Tx) GetDevice(ctx context.Context, id string) (Device, error) {
	row := t.conn.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE id = ?`, id)

	device, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, errs.New(errs.CodeDeviceNotFound, "no device %q in the pool", id)
	}

	return device, err
}

// ListDevices returns every known device ordered by name.
func (t *Tx) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := t.conn.QueryContext(ctx, `SELECT `+deviceColumns+` FROM devices ORDER BY name, id`)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "listing devices")
	}
	defer rows.Close()

	devices := []Device{}

	for rows.Next() {
		device, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}

		devices = append(devices, device)
	}

	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "listing devices")
	}

	return devices, nil
}

// DeviceFilter narrows candidate devices for an acquire.
type DeviceFilter struct {
	Platform   string
	DeviceType string
	Runtime    string
}

// FindAvailableDevice returns the best borrowable device with no live lease.
// Booted devices win, so a reused simulator is ready sooner.
func (t *Tx) FindAvailableDevice(ctx context.Context, filter DeviceFilter) (Device, bool, error) {
	query := `SELECT ` + deviceColumns + ` FROM devices
		WHERE borrowable = 1
		  AND available = 1
		  AND missing_at IS NULL
		  AND platform = ?
		  AND NOT EXISTS (SELECT 1 FROM leases WHERE leases.device_id = devices.id AND leases.released_at IS NULL)`
	args := []any{filter.Platform}

	if filter.DeviceType != "" {
		query += " AND device_type = ?"
		args = append(args, filter.DeviceType)
	}

	if filter.Runtime != "" {
		query += " AND runtime = ?"
		args = append(args, filter.Runtime)
	}

	query += ` ORDER BY CASE state WHEN 'booted' THEN 0 WHEN 'booting' THEN 1 ELSE 2 END,
		CASE provenance WHEN 'pool' THEN 0 ELSE 1 END, updated_at ASC LIMIT 1`

	device, err := scanDevice(t.conn.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, nil
	}

	if err != nil {
		return Device{}, false, err
	}

	return device, true, nil
}

// CountDevices counts non-missing devices with the given provenance, which is
// what pool capacity is measured against.
func (t *Tx) CountDevices(ctx context.Context, platform, provenance string) (int, error) {
	const query = `SELECT COUNT(*) FROM devices
		WHERE platform = ? AND provenance = ? AND missing_at IS NULL`

	var count int
	if err := t.conn.QueryRowContext(ctx, query, platform, provenance).Scan(&count); err != nil {
		return 0, errs.Wrap(err, errs.CodeState, "counting devices")
	}

	return count, nil
}

// SetBorrowable marks whether the pool may hand a device out.
func (t *Tx) SetBorrowable(ctx context.Context, id string, borrowable bool, now time.Time) error {
	const statement = `UPDATE devices SET borrowable = ?, updated_at = ? WHERE id = ?`

	if _, err := t.conn.ExecContext(ctx, statement, borrowable, formatTime(now), id); err != nil {
		return errs.Wrap(err, errs.CodeState, "updating device %q", id)
	}

	return nil
}

// SetDeviceState records an observed lifecycle state.
func (t *Tx) SetDeviceState(ctx context.Context, id, state string, now time.Time) error {
	const statement = `UPDATE devices SET state = ?, updated_at = ? WHERE id = ?`

	if _, err := t.conn.ExecContext(ctx, statement, state, formatTime(now), id); err != nil {
		return errs.Wrap(err, errs.CodeState, "updating device %q state", id)
	}

	return nil
}

// DeleteDevice removes a device and, by cascade, its lease history.
func (t *Tx) DeleteDevice(ctx context.Context, id string) error {
	if _, err := t.conn.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id); err != nil {
		return errs.Wrap(err, errs.CodeState, "deleting device %q", id)
	}

	return nil
}

// InsertLease records a new live lease. A unique-index violation means another
// process won the same device or Metro port first.
func (t *Tx) InsertLease(ctx context.Context, lease Lease) error {
	const statement = `INSERT INTO leases (
		id, device_id, borrower, metadata, acquired_at, expires_at, released_at, release_reason
	) VALUES (?, ?, ?, ?, ?, ?, NULL, '')`

	metadata, err := encodeMetadata(lease.Metadata)
	if err != nil {
		return err
	}

	_, err = t.conn.ExecContext(
		ctx,
		statement,
		lease.ID,
		lease.DeviceID,
		lease.Borrower,
		metadata,
		formatTime(lease.AcquiredAt),
		formatTime(lease.ExpiresAt),
	)
	if err == nil {
		return nil
	}

	return leaseWriteError(err, lease.DeviceID, lease.Meta(MetaMetroPort))
}

// leaseWriteError maps a unique-index violation onto the code that explains
// which claim was already taken.
func leaseWriteError(err error, deviceID, metroPort string) error {
	message := err.Error()

	switch {
	case strings.Contains(message, "leases.device_id"):
		return errs.New(errs.CodeDeviceLeased, "device %s is already leased", deviceID)
	case strings.Contains(message, "idx_leases_live_metro"), strings.Contains(message, "leases.metadata"):
		return errs.New(errs.CodeMetroPortConflict, "metro port %s is already recorded on a live lease", metroPort).
			WithDetail("metro_port", metroPort)
	default:
		return errs.Wrap(err, errs.CodeState, "recording lease for device %s", deviceID)
	}
}

// GetLease returns one lease, or a lease_not_found error.
func (t *Tx) GetLease(ctx context.Context, id string) (Lease, error) {
	row := t.conn.QueryRowContext(ctx, `SELECT `+leaseColumns+` FROM leases WHERE id = ?`, id)

	lease, err := scanLease(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, errs.New(errs.CodeLeaseNotFound, "no lease %q in the pool", id)
	}

	return lease, err
}

// LiveLeaseForDevice returns the unreleased lease on a device, if any.
func (t *Tx) LiveLeaseForDevice(ctx context.Context, deviceID string) (Lease, bool, error) {
	const query = `SELECT ` + leaseColumns + ` FROM leases WHERE device_id = ? AND released_at IS NULL`

	lease, err := scanLease(t.conn.QueryRowContext(ctx, query, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}

	if err != nil {
		return Lease{}, false, err
	}

	return lease, true, nil
}

// LiveLeaseForMetroPort returns the unreleased lease recording a Metro port.
func (t *Tx) LiveLeaseForMetroPort(ctx context.Context, port string) (Lease, bool, error) {
	const query = `SELECT ` + leaseColumns + ` FROM leases
		WHERE ` + metroPortPath + ` = ? AND released_at IS NULL`

	lease, err := scanLease(t.conn.QueryRowContext(ctx, query, port))
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, false, nil
	}

	if err != nil {
		return Lease{}, false, err
	}

	return lease, true, nil
}

// ListLiveLeases returns every unreleased lease.
func (t *Tx) ListLiveLeases(ctx context.Context) ([]Lease, error) {
	const query = `SELECT ` + leaseColumns + ` FROM leases WHERE released_at IS NULL ORDER BY acquired_at`

	rows, err := t.conn.QueryContext(ctx, query)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "listing leases")
	}
	defer rows.Close()

	leases := []Lease{}

	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			return nil, err
		}

		leases = append(leases, lease)
	}

	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "listing leases")
	}

	return leases, nil
}

// ReleaseLease closes a live lease. It reports whether a row changed, so an
// already-released lease can be answered idempotently.
func (t *Tx) ReleaseLease(ctx context.Context, id string, reason string, now time.Time) (bool, error) {
	const statement = `UPDATE leases SET released_at = ?, release_reason = ?
		WHERE id = ? AND released_at IS NULL`

	result, err := t.conn.ExecContext(ctx, statement, formatTime(now), reason, id)
	if err != nil {
		return false, errs.Wrap(err, errs.CodeState, "releasing lease %q", id)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, errs.Wrap(err, errs.CodeState, "releasing lease %q", id)
	}

	return affected > 0, nil
}

// ReleaseLeasesForDevice closes every live lease on a device.
func (t *Tx) ReleaseLeasesForDevice(ctx context.Context, deviceID, reason string, now time.Time) (int, error) {
	const statement = `UPDATE leases SET released_at = ?, release_reason = ?
		WHERE device_id = ? AND released_at IS NULL`

	result, err := t.conn.ExecContext(ctx, statement, formatTime(now), reason, deviceID)
	if err != nil {
		return 0, errs.Wrap(err, errs.CodeState, "releasing leases on device %q", deviceID)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, errs.Wrap(err, errs.CodeState, "releasing leases on device %q", deviceID)
	}

	return int(affected), nil
}

// ExpireLeases releases every lease whose TTL elapsed and returns them. An
// expired lease ID can then never affect the next borrower.
func (t *Tx) ExpireLeases(ctx context.Context, now time.Time) ([]Lease, error) {
	const query = `SELECT ` + leaseColumns + ` FROM leases
		WHERE released_at IS NULL AND expires_at <= ?`

	rows, err := t.conn.QueryContext(ctx, query, formatTime(now))
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "finding expired leases")
	}

	expired := []Lease{}

	for rows.Next() {
		lease, err := scanLease(rows)
		if err != nil {
			rows.Close()

			return nil, err
		}

		expired = append(expired, lease)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "finding expired leases")
	}

	for _, lease := range expired {
		if _, err := t.ReleaseLease(ctx, lease.ID, ReleaseReasonExpired, now); err != nil {
			return nil, err
		}
	}

	return expired, nil
}

// RenewLease extends a live lease. It reports whether the lease was still live.
func (t *Tx) RenewLease(ctx context.Context, id string, expiresAt, now time.Time) (bool, error) {
	const statement = `UPDATE leases SET expires_at = ?
		WHERE id = ? AND released_at IS NULL AND expires_at > ?`

	result, err := t.conn.ExecContext(ctx, statement, formatTime(expiresAt), id, formatTime(now))
	if err != nil {
		return false, errs.Wrap(err, errs.CodeState, "renewing lease %q", id)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, errs.Wrap(err, errs.CodeState, "renewing lease %q", id)
	}

	return affected > 0, nil
}

// SetLeaseMetadata replaces the whole metadata document on a lease.
func (t *Tx) SetLeaseMetadata(ctx context.Context, id string, metadata map[string]string) error {
	const statement = `UPDATE leases SET metadata = ? WHERE id = ?`

	encoded, err := encodeMetadata(metadata)
	if err != nil {
		return err
	}

	if _, err := t.conn.ExecContext(ctx, statement, encoded, id); err != nil {
		return leaseWriteError(err, id, metadata[MetaMetroPort])
	}

	return nil
}

// InsertOperation reserves externally visible work before it is performed.
func (t *Tx) InsertOperation(ctx context.Context, operation Operation) error {
	const statement = `INSERT INTO operations (
		id, kind, state, device_id, device_name, borrower, started_at, expires_at, finished_at, error
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, '')`

	_, err := t.conn.ExecContext(
		ctx,
		statement,
		operation.ID,
		operation.Kind,
		operation.State,
		nullableString(operation.DeviceID),
		nullableString(operation.DeviceName),
		operation.Borrower,
		formatTime(operation.StartedAt),
		formatTime(operation.ExpiresAt),
	)
	if err != nil {
		return errs.Wrap(err, errs.CodeState, "recording %s operation", operation.Kind)
	}

	return nil
}

// FinishOperation records the outcome of a reserved operation.
func (t *Tx) FinishOperation(ctx context.Context, id, state, deviceID, failure string, now time.Time) error {
	const statement = `UPDATE operations
		SET state = ?, device_id = COALESCE(?, device_id), finished_at = ?, error = ?
		WHERE id = ?`

	_, err := t.conn.ExecContext(ctx, statement, state, nullableString(deviceID), formatTime(now), failure, id)
	if err != nil {
		return errs.Wrap(err, errs.CodeState, "finishing operation %q", id)
	}

	return nil
}

// PendingOperations returns reservations that are still open.
func (t *Tx) PendingOperations(ctx context.Context) ([]Operation, error) {
	const query = `SELECT id, kind, state, device_id, device_name, borrower,
		started_at, expires_at, finished_at, error
		FROM operations WHERE state = ? ORDER BY started_at`

	rows, err := t.conn.QueryContext(ctx, query, OperationPending)
	if err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "listing operations")
	}
	defer rows.Close()

	operations := []Operation{}

	for rows.Next() {
		var (
			operation  Operation
			deviceID   sql.NullString
			deviceName sql.NullString
			startedAt  string
			expiresAt  string
			finishedAt sql.NullString
		)

		err := rows.Scan(
			&operation.ID,
			&operation.Kind,
			&operation.State,
			&deviceID,
			&deviceName,
			&operation.Borrower,
			&startedAt,
			&expiresAt,
			&finishedAt,
			&operation.Error,
		)
		if err != nil {
			return nil, errs.Wrap(err, errs.CodeState, "scanning operation")
		}

		operation.DeviceID = deviceID.String
		operation.DeviceName = deviceName.String

		if operation.StartedAt, err = parseTime(startedAt); err != nil {
			return nil, err
		}

		if operation.ExpiresAt, err = parseTime(expiresAt); err != nil {
			return nil, err
		}

		if operation.FinishedAt, err = nullableTime(finishedAt); err != nil {
			return nil, err
		}

		operations = append(operations, operation)
	}

	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "listing operations")
	}

	return operations, nil
}

// scanner covers both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(source scanner) (Device, error) {
	var (
		device    Device
		missingAt sql.NullString
		createdAt string
		updatedAt string
	)

	err := source.Scan(
		&device.ID,
		&device.Platform,
		&device.Name,
		&device.DeviceType,
		&device.Runtime,
		&device.State,
		&device.Provenance,
		&device.Borrowable,
		&device.Available,
		&missingAt,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, err
		}

		return Device{}, errs.Wrap(err, errs.CodeState, "scanning device")
	}

	if device.MissingAt, err = nullableTime(missingAt); err != nil {
		return Device{}, err
	}

	if device.CreatedAt, err = parseTime(createdAt); err != nil {
		return Device{}, err
	}

	if device.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Device{}, err
	}

	return device, nil
}

func scanLease(source scanner) (Lease, error) {
	var (
		lease      Lease
		metadata   sql.NullString
		acquiredAt string
		expiresAt  string
		releasedAt sql.NullString
	)

	err := source.Scan(
		&lease.ID,
		&lease.DeviceID,
		&lease.Borrower,
		&metadata,
		&acquiredAt,
		&expiresAt,
		&releasedAt,
		&lease.ReleaseReason,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Lease{}, err
		}

		return Lease{}, errs.Wrap(err, errs.CodeState, "scanning lease")
	}

	if lease.Metadata, err = decodeMetadata(metadata.String); err != nil {
		return Lease{}, err
	}

	if lease.AcquiredAt, err = parseTime(acquiredAt); err != nil {
		return Lease{}, err
	}

	if lease.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return Lease{}, err
	}

	if lease.ReleasedAt, err = nullableTime(releasedAt); err != nil {
		return Lease{}, err
	}

	return lease, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}

	return value
}

// encodeMetadata stores the bag as a JSON object. An empty bag is stored as
// `{}` rather than NULL, so the Metro-port index sees a consistent shape.
func encodeMetadata(metadata map[string]string) (string, error) {
	if len(metadata) == 0 {
		return "{}", nil
	}

	encoded, err := json.Marshal(metadata)
	if err != nil {
		return "", errs.Wrap(err, errs.CodeState, "encoding lease metadata")
	}

	return string(encoded), nil
}

func decodeMetadata(raw string) (map[string]string, error) {
	if raw == "" || raw == "{}" {
		return map[string]string{}, nil
	}

	metadata := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil, errs.Wrap(err, errs.CodeState, "decoding lease metadata %q", raw)
	}

	return metadata, nil
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}
