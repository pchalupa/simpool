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
// wants to remember about the device lives in Metadata, which is free-form and
// stored as given; a lease needs no keys at all.
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

// Live reports whether the lease is still held at the given instant.
func (l Lease) Live(now time.Time) bool {
	return l.ReleasedAt == nil && l.ExpiresAt.After(now)
}
