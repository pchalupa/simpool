// Package pool orchestrates simpool: it reconciles persisted state against the
// platform and allocates leases atomically.
package pool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/pchalupa/simpool/internal/config"
	"github.com/pchalupa/simpool/internal/provider"
	"github.com/pchalupa/simpool/internal/store"
)

// Pool is the orchestration entry point. One instance serves one CLI command.
type Pool struct {
	cfg      config.Config
	store    *store.Store
	provider provider.Provider
	now      func() time.Time
}

// New wires a pool over a store and a platform provider.
func New(cfg config.Config, state *store.Store, platform provider.Provider) *Pool {
	return &Pool{
		cfg:      cfg,
		store:    state,
		provider: platform,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the time source. Tests use it to exercise expiry.
func (p *Pool) SetClock(clock func() time.Time) {
	p.now = clock
}

// Config exposes the resolved configuration for reporting commands.
func (p *Pool) Config() config.Config {
	return p.cfg
}

// DeviceView is a device together with the lease currently held on it.
type DeviceView struct {
	Device store.Device `json:"device"`
	Lease  *store.Lease `json:"lease,omitempty"`
}

// ReconcileReport describes what reconciliation changed.
type ReconcileReport struct {
	MissingDevices   []string `json:"missing_devices"`
	ExpiredLeases    []string `json:"expired_leases"`
	DiscoveredDevice []string `json:"discovered_devices"`
}

// Reconcile compares persisted state against the platform before anything is
// allocated. simctl is authoritative for device existence and state; the
// database stays authoritative for leases and provenance.
func (p *Pool) Reconcile(ctx context.Context) (ReconcileReport, error) {
	observed, err := p.provider.ListDevices(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}

	report := ReconcileReport{
		MissingDevices:   []string{},
		ExpiredLeases:    []string{},
		DiscoveredDevice: []string{},
	}

	now := p.now()

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		known, err := knownDeviceIDs(ctx, tx)
		if err != nil {
			return err
		}

		presentIDs := make([]string, 0, len(observed))

		for _, device := range observed {
			presentIDs = append(presentIDs, device.ID)

			observation := store.Observation{
				ID:         device.ID,
				Platform:   device.Platform,
				Name:       device.Name,
				DeviceType: device.DeviceType,
				Runtime:    device.Runtime,
				State:      string(device.State),
				Available:  device.Available,
				Provenance: store.ProvenanceExternal,
				Borrowable: p.cfg.BorrowUnmanaged,
			}

			if err := tx.ObserveDevice(ctx, observation, now); err != nil {
				return err
			}

			if !known[device.ID] {
				report.DiscoveredDevice = append(report.DiscoveredDevice, device.ID)
			}
		}

		missing, err := tx.MarkDeviceMissing(ctx, presentIDs, now)
		if err != nil {
			return err
		}

		report.MissingDevices = missing

		// A device that vanished cannot back a lease.
		for _, id := range missing {
			if _, err := tx.ReleaseLeasesForDevice(ctx, id, store.ReleaseReasonDeviceMissing, now); err != nil {
				return err
			}
		}

		expired, err := tx.ExpireLeases(ctx, now)
		if err != nil {
			return err
		}

		for _, lease := range expired {
			report.ExpiredLeases = append(report.ExpiredLeases, lease.ID)
		}

		return nil
	})
	if err != nil {
		return ReconcileReport{}, err
	}

	return report, nil
}

// ExpireLeases performs the cheap half of reconciliation — no platform call —
// for commands that act on a lease the caller already holds.
func (p *Pool) ExpireLeases(ctx context.Context) error {
	now := p.now()

	return p.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.ExpireLeases(ctx, now)

		return err
	})
}

// List returns every known device with its live lease.
func (p *Pool) List(ctx context.Context) ([]DeviceView, error) {
	if _, err := p.Reconcile(ctx); err != nil {
		return nil, err
	}

	views := []DeviceView{}

	err := p.store.Read(ctx, func(tx *store.Tx) error {
		devices, err := tx.ListDevices(ctx)
		if err != nil {
			return err
		}

		leases, err := tx.ListLiveLeases(ctx)
		if err != nil {
			return err
		}

		byDevice := make(map[string]store.Lease, len(leases))
		for _, lease := range leases {
			byDevice[lease.DeviceID] = lease
		}

		for _, device := range devices {
			view := DeviceView{Device: device}

			if lease, ok := byDevice[device.ID]; ok {
				view.Lease = &lease
			}

			views = append(views, view)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return views, nil
}

func knownDeviceIDs(ctx context.Context, tx *store.Tx) (map[string]bool, error) {
	devices, err := tx.ListDevices(ctx)
	if err != nil {
		return nil, err
	}

	known := make(map[string]bool, len(devices))
	for _, device := range devices {
		known[device.ID] = true
	}

	return known, nil
}

// newID returns an opaque identifier for leases.
func newID(prefix string) string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand does not fail in practice; fall back to a timestamp so a
		// CLI invocation never dies on identifier generation.
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}

	return prefix + "_" + hex.EncodeToString(buffer)
}
