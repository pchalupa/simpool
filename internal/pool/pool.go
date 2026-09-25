// Package pool orchestrates simpool: it reconciles persisted state against the
// platform, allocates leases atomically and guards pool capacity and deletion.
package pool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pchalupa/simpool/internal/config"
	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/provider"
	"github.com/pchalupa/simpool/internal/store"
)

// namePrefix marks simulators simpool created. The suffix is the identifier of
// the operation that reserved the create, so an interrupted create is
// recognisable on the next invocation.
const namePrefix = "simpool-"

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
	AdoptedDevices   []string `json:"adopted_devices"`
	MissingDevices   []string `json:"missing_devices"`
	ExpiredLeases    []string `json:"expired_leases"`
	AbandonedOps     []string `json:"abandoned_operations"`
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
		AdoptedDevices:   []string{},
		MissingDevices:   []string{},
		ExpiredLeases:    []string{},
		AbandonedOps:     []string{},
		DiscoveredDevice: []string{},
	}

	now := p.now()

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		known, err := knownDeviceIDs(ctx, tx)
		if err != nil {
			return err
		}

		pending, err := tx.PendingOperations(ctx)
		if err != nil {
			return err
		}

		// A create that was interrupted after simctl made the device leaves an
		// orphan named after its operation; adopt it instead of leaking it.
		orphanOps := map[string]store.Operation{}
		for _, operation := range pending {
			if operation.Kind == store.OperationCreate && operation.DeviceName != "" {
				orphanOps[operation.DeviceName] = operation
			}
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

			operation, isOrphan := orphanOps[device.Name]
			if isOrphan && !known[device.ID] {
				observation.Provenance = store.ProvenancePool
				observation.Borrowable = true
			}

			if err := tx.ObserveDevice(ctx, observation, now); err != nil {
				return err
			}

			switch {
			case isOrphan && !known[device.ID]:
				report.AdoptedDevices = append(report.AdoptedDevices, device.ID)

				if err := tx.FinishOperation(ctx, operation.ID, store.OperationDone, device.ID, "adopted by reconciliation", now); err != nil {
					return err
				}

				delete(orphanOps, device.Name)
			case !known[device.ID]:
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

		// Whatever is left pending past its deadline never produced a device.
		for _, operation := range pending {
			if operation.ExpiresAt.After(now) {
				continue
			}

			if _, stillOrphan := orphanOps[operation.DeviceName]; !stillOrphan && operation.DeviceName != "" {
				continue
			}

			if err := tx.FinishOperation(ctx, operation.ID, store.OperationFailed, "", "abandoned: the process reserving it did not finish", now); err != nil {
				return err
			}

			report.AbandonedOps = append(report.AbandonedOps, operation.ID)
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

// newID returns an opaque identifier for leases and operations.
func newID(prefix string) string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		// crypto/rand does not fail in practice; fall back to a timestamp so a
		// CLI invocation never dies on identifier generation.
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}

	return prefix + "_" + hex.EncodeToString(buffer)
}

// canonicalWorktree normalises a worktree path so the same checkout is recorded
// identically by every agent. It is metadata, never proof of ownership.
func canonicalWorktree(path string) (string, error) {
	if path == "" {
		return "", nil
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errs.Wrap(err, errs.CodeInvalidArgs, "resolving worktree %q", path)
	}

	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		// A path that does not exist yet is still recorded, just uncanonicalised.
		return filepath.Clean(absolute), nil
	}

	return resolved, nil
}
