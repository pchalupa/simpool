// Package provider defines the platform boundary simpool allocates devices
// through. The iOS implementation shells out to `xcrun simctl`; an Android
// implementation can be added without touching the lease layer.
package provider

import (
	"context"
	"time"
)

// Platform identifiers.
const (
	PlatformIOS     = "ios"
	PlatformAndroid = "android"
)

// State is the observed lifecycle state of a device, normalised across
// platforms.
type State string

const (
	StateUnknown  State = "unknown"
	StateShutdown State = "shutdown"
	StateBooting  State = "booting"
	StateBooted   State = "booted"
)

// Device is a platform device as the provider sees it right now. The provider
// is authoritative for existence and runtime state; the database is
// authoritative for leases and provenance.
type Device struct {
	ID         string `json:"id"`
	Platform   string `json:"platform"`
	Name       string `json:"name"`
	DeviceType string `json:"device_type"`
	Runtime    string `json:"runtime"`
	State      State  `json:"state"`
	// Available is false for devices whose runtime is not installed or is
	// otherwise unusable.
	Available bool `json:"available"`
}

// DeviceType is a creatable hardware profile (e.g. an iPhone model).
type DeviceType struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Runtime is an installed OS version a device can be created against.
type Runtime struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
}

// CreateRequest describes a device to create. Name is chosen by simpool and
// encodes the reserving operation so an interrupted create can be reconciled.
type CreateRequest struct {
	Name       string
	DeviceType string
	Runtime    string
}

// Provider is the set of operations simpool needs from a platform toolchain.
type Provider interface {
	// Platform returns the identifier devices of this provider carry.
	Platform() string
	// Preflight reports whether the platform toolchain is usable, with a
	// human-readable description of what was found.
	Preflight(ctx context.Context) (string, error)
	ListDevices(ctx context.Context) ([]Device, error)
	ListDeviceTypes(ctx context.Context) ([]DeviceType, error)
	ListRuntimes(ctx context.Context) ([]Runtime, error)
	// Inspect returns a single device, or an error with code
	// errs.CodeDeviceNotFound when the platform no longer knows it.
	Inspect(ctx context.Context, id string) (Device, error)
	Create(ctx context.Context, req CreateRequest) (Device, error)
	Boot(ctx context.Context, id string) error
	// WaitReady blocks until the device finished booting or timeout elapses.
	WaitReady(ctx context.Context, id string, timeout time.Duration) error
	Shutdown(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
}
