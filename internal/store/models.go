package store

import "time"

// Provenance records how a device entered the pool.
const (
	// ProvenancePool marks a device simpool created and may therefore delete.
	ProvenancePool = "pool"
	// ProvenanceExternal marks a device discovered on the host. simpool never
	// deletes these.
	ProvenanceExternal = "external"
)

// Release reasons recorded on a lease when it stops being live.
const (
	ReleaseReasonReleased      = "released"
	ReleaseReasonExpired       = "expired"
	ReleaseReasonReclaimed     = "reclaimed"
	ReleaseReasonDeviceMissing = "device_missing"
	ReleaseReasonDeviceDeleted = "device_deleted"
)

// Operation kinds and states used to reserve externally visible work before it
// happens, so an interrupted CLI process can be reconciled later.
const (
	OperationCreate = "create"

	OperationPending = "pending"
	OperationDone    = "done"
	OperationFailed  = "failed"
)

// Device is the persisted record of a simulator.
type Device struct {
	ID         string     `json:"id"`
	Platform   string     `json:"platform"`
	Name       string     `json:"name"`
	DeviceType string     `json:"device_type"`
	Runtime    string     `json:"runtime"`
	State      string     `json:"state"`
	Provenance string     `json:"provenance"`
	Borrowable bool       `json:"borrowable"`
	Available  bool       `json:"available"`
	MissingAt  *time.Time `json:"missing_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Managed reports whether simpool owns the device's lifecycle.
func (d Device) Managed() bool {
	return d.Provenance == ProvenancePool
}

// Missing reports whether the device is recorded but absent from the platform.
func (d Device) Missing() bool {
	return d.MissingAt != nil
}

// Lease is a borrower's exclusive claim on a device. Everything the borrower
// wants to remember about the device lives in Metadata, which is free-form: a
// lease needs no worktree, no Metro port and no keys at all.
type Lease struct {
	ID            string            `json:"id"`
	DeviceID      string            `json:"device_id"`
	Borrower      string            `json:"borrower"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AcquiredAt    time.Time         `json:"acquired_at"`
	ExpiresAt     time.Time         `json:"expires_at"`
	ReleasedAt    *time.Time        `json:"released_at,omitempty"`
	ReleaseReason string            `json:"release_reason,omitempty"`
}

// Well-known metadata keys. They are ordinary keys: simpool reads them where it
// can act on them, and a lease that carries neither is perfectly normal.
const (
	// MetaWorktree is the canonical path of the checkout a lease belongs to.
	MetaWorktree = "worktree"
	// MetaMetroPort is the React Native Metro port the borrower will serve on.
	// Two live leases may not record the same one.
	MetaMetroPort = "metro_port"
)

// Meta returns one metadata value, or the empty string when it is not set.
func (l Lease) Meta(key string) string {
	return l.Metadata[key]
}

// Live reports whether the lease is still held at the given instant.
func (l Lease) Live(now time.Time) bool {
	return l.ReleasedAt == nil && l.ExpiresAt.After(now)
}

// Operation is a reservation for work that touches the platform.
type Operation struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	State      string     `json:"state"`
	DeviceID   string     `json:"device_id,omitempty"`
	DeviceName string     `json:"device_name,omitempty"`
	Borrower   string     `json:"borrower,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}
