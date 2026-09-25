package pool

import (
	"context"

	"github.com/strv/simpool/internal/config"

	"github.com/strv/simpool/internal/errs"
	"github.com/strv/simpool/internal/provider"
	"github.com/strv/simpool/internal/store"
)

// CreateResult reports a pool-owned device that was just created.
type CreateResult struct {
	Device store.Device `json:"device"`
}

// Create adds a device to the pool without leasing it. This is pool
// maintenance, not borrowing.
func (p *Pool) Create(ctx context.Context, deviceType, runtime string) (CreateResult, error) {
	if _, err := p.Reconcile(ctx); err != nil {
		return CreateResult{}, err
	}

	resolvedType, err := p.resolveDeviceType(ctx, deviceType)
	if err != nil {
		return CreateResult{}, err
	}

	resolvedRuntime, err := p.resolveRuntime(ctx, runtime)
	if err != nil {
		return CreateResult{}, err
	}

	device, operationID, err := p.createDevice(ctx, resolvedType, resolvedRuntime, p.cfg.Borrower)
	if err != nil {
		return CreateResult{}, err
	}

	now := p.now()

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		return tx.FinishOperation(ctx, operationID, store.OperationDone, device.ID, "", now)
	})
	if err != nil {
		return CreateResult{}, err
	}

	return CreateResult{Device: device}, nil
}

// createDevice reserves capacity, creates the simulator and records it. The
// returned operation stays pending so the caller can commit its own follow-up
// work (such as a lease) in the same transaction that closes it.
func (p *Pool) createDevice(ctx context.Context, deviceType, runtime, borrower string) (store.Device, string, error) {
	resolvedType, resolvedRuntime, err := p.creationDefaults(ctx, deviceType, runtime)
	if err != nil {
		return store.Device{}, "", err
	}

	operationID := newID("op")
	deviceName := namePrefix + operationID
	now := p.now()

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		if err := p.checkCapacity(ctx, tx); err != nil {
			return err
		}

		return tx.InsertOperation(ctx, store.Operation{
			ID:         operationID,
			Kind:       store.OperationCreate,
			State:      store.OperationPending,
			DeviceName: deviceName,
			Borrower:   borrower,
			StartedAt:  now,
			ExpiresAt:  now.Add(p.cfg.OpTimeout),
		})
	})
	if err != nil {
		return store.Device{}, "", err
	}

	created, createErr := p.provider.Create(ctx, provider.CreateRequest{
		Name:       deviceName,
		DeviceType: resolvedType,
		Runtime:    resolvedRuntime,
	})
	if createErr != nil {
		// The platform may still have made the device before failing. Adopt it
		// rather than leaking a simulator nothing owns.
		orphan, found, lookupErr := p.findByName(ctx, deviceName)
		if lookupErr != nil || !found {
			failErr := p.store.Write(ctx, func(tx *store.Tx) error {
				return tx.FinishOperation(ctx, operationID, store.OperationFailed, "", createErr.Error(), p.now())
			})
			if failErr != nil {
				return store.Device{}, "", failErr
			}

			return store.Device{}, "", createErr
		}

		created = orphan
	}

	var recorded store.Device

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		observation := store.Observation{
			ID:         created.ID,
			Platform:   created.Platform,
			Name:       created.Name,
			DeviceType: created.DeviceType,
			Runtime:    created.Runtime,
			State:      string(created.State),
			Available:  true,
			Provenance: store.ProvenancePool,
			Borrowable: true,
		}

		if err := tx.ObserveDevice(ctx, observation, p.now()); err != nil {
			return err
		}

		recorded, err = tx.GetDevice(ctx, created.ID)

		return err
	})
	if err != nil {
		return store.Device{}, "", err
	}

	return recorded, operationID, nil
}

// creationDefaults fills in the device type and runtime when the caller did not
// name one, preferring explicit configuration over the built-in heuristic.
func (p *Pool) creationDefaults(ctx context.Context, deviceType, runtime string) (string, string, error) {
	var err error

	if deviceType == "" {
		deviceType, err = p.resolveDeviceType(ctx, p.cfg.DefaultDeviceType)
		if err != nil {
			return "", "", err
		}
	}

	if deviceType == "" {
		if deviceType, err = p.defaultDeviceType(ctx); err != nil {
			return "", "", err
		}
	}

	if runtime == "" {
		runtime, err = p.resolveRuntime(ctx, p.cfg.DefaultRuntime)
		if err != nil {
			return "", "", err
		}
	}

	if runtime == "" {
		if runtime, err = p.defaultRuntime(ctx); err != nil {
			return "", "", err
		}
	}

	return deviceType, runtime, nil
}

// checkCapacity refuses to grow the pool past its configured bound. Creates
// that are still in flight count, so concurrent callers cannot overshoot.
func (p *Pool) checkCapacity(ctx context.Context, tx *store.Tx) error {
	owned, err := tx.CountDevices(ctx, p.provider.Platform(), store.ProvenancePool)
	if err != nil {
		return err
	}

	pending, err := tx.PendingOperations(ctx)
	if err != nil {
		return err
	}

	inFlight := 0

	for _, operation := range pending {
		if operation.Kind == store.OperationCreate {
			inFlight++
		}
	}

	if owned+inFlight < p.cfg.MaxDevices {
		return nil
	}

	return errs.New(
		errs.CodeNoCapacity,
		"pool is at capacity: %d pool-owned %s devices (%d create(s) in flight), limit %d",
		owned,
		p.provider.Platform(),
		inFlight,
		p.cfg.MaxDevices,
	).
		WithDetail("pool_devices", owned).
		WithDetail("creates_in_flight", inFlight).
		WithDetail("max_devices", p.cfg.MaxDevices).
		WithDetail("hint", "release a lease, delete an idle pool device, or raise "+config.EnvMaxDevices)
}

// DeleteResult reports the outcome of removing a device from the pool.
type DeleteResult struct {
	DeviceID string `json:"device_id"`
	Deleted  bool   `json:"deleted"`
}

// Delete removes a pool-owned device. Externally created simulators are never
// deleted, and a live lease blocks deletion unless force is set.
func (p *Pool) Delete(ctx context.Context, deviceID string, force bool) (DeleteResult, error) {
	if _, err := p.Reconcile(ctx); err != nil {
		return DeleteResult{}, err
	}

	var device store.Device

	now := p.now()

	err := p.store.Write(ctx, func(tx *store.Tx) error {
		found, err := tx.GetDevice(ctx, deviceID)
		if err != nil {
			return err
		}

		device = found

		if !device.Managed() {
			return errs.New(errs.CodeDeviceUnmanaged, "device %s was created outside simpool and will not be deleted", deviceID).
				WithDetail("provenance", device.Provenance)
		}

		lease, held, err := tx.LiveLeaseForDevice(ctx, deviceID)
		if err != nil {
			return err
		}

		if held && !force {
			return errs.New(errs.CodeDeviceLeased, "device %s is leased until %s; release lease %s or pass --force", deviceID, lease.ExpiresAt.Local().Format("2006-01-02 15:04"), lease.ID).
				WithDetail("lease_id", lease.ID).
				WithDetail("borrower", lease.Borrower).
				WithDetail("expires_at", lease.ExpiresAt)
		}

		// Take the device out of circulation before touching the platform, so no
		// concurrent acquire can lease a device that is about to disappear.
		if err := tx.SetBorrowable(ctx, deviceID, false, now); err != nil {
			return err
		}

		_, err = tx.ReleaseLeasesForDevice(ctx, deviceID, store.ReleaseReasonDeviceDeleted, now)

		return err
	})
	if err != nil {
		return DeleteResult{}, err
	}

	if !device.Missing() {
		if err := p.deleteOnPlatform(ctx, deviceID); err != nil {
			return DeleteResult{}, err
		}
	}

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		return tx.DeleteDevice(ctx, deviceID)
	})
	if err != nil {
		return DeleteResult{}, err
	}

	return DeleteResult{
		DeviceID: deviceID,
		Deleted:  true,
	}, nil
}

// deleteOnPlatform shuts the device down and deletes it, restoring
// borrowability if the platform refuses so the device is not stranded.
func (p *Pool) deleteOnPlatform(ctx context.Context, deviceID string) error {
	if err := p.provider.Shutdown(ctx, deviceID); err != nil {
		return p.restoreBorrowable(ctx, deviceID, err)
	}

	if err := p.provider.Delete(ctx, deviceID); err != nil {
		return p.restoreBorrowable(ctx, deviceID, err)
	}

	return nil
}

func (p *Pool) restoreBorrowable(ctx context.Context, deviceID string, cause error) error {
	restoreErr := p.store.Write(ctx, func(tx *store.Tx) error {
		return tx.SetBorrowable(ctx, deviceID, true, p.now())
	})
	if restoreErr != nil {
		return restoreErr
	}

	return cause
}

// ShutdownResult reports a device that was powered off.
type ShutdownResult struct {
	DeviceID string `json:"device_id"`
	State    string `json:"state"`
}

// Shutdown powers a device off without touching its lease or provenance.
func (p *Pool) Shutdown(ctx context.Context, deviceID string) (ShutdownResult, error) {
	if _, err := p.Reconcile(ctx); err != nil {
		return ShutdownResult{}, err
	}

	err := p.store.Read(ctx, func(tx *store.Tx) error {
		_, err := tx.GetDevice(ctx, deviceID)

		return err
	})
	if err != nil {
		return ShutdownResult{}, err
	}

	if err := p.provider.Shutdown(ctx, deviceID); err != nil {
		return ShutdownResult{}, err
	}

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		return tx.SetDeviceState(ctx, deviceID, string(provider.StateShutdown), p.now())
	})
	if err != nil {
		return ShutdownResult{}, err
	}

	return ShutdownResult{
		DeviceID: deviceID,
		State:    string(provider.StateShutdown),
	}, nil
}

// ImportResult reports a borrowability change on an existing device.
type ImportResult struct {
	Device store.Device `json:"device"`
}

// Import opts an externally created simulator into the pool, or (with undo)
// takes it back out. simpool never deletes such a device either way.
func (p *Pool) Import(ctx context.Context, deviceID string, undo bool) (ImportResult, error) {
	if _, err := p.Reconcile(ctx); err != nil {
		return ImportResult{}, err
	}

	var device store.Device

	err := p.store.Write(ctx, func(tx *store.Tx) error {
		found, err := tx.GetDevice(ctx, deviceID)
		if err != nil {
			return err
		}

		if undo {
			lease, held, err := tx.LiveLeaseForDevice(ctx, deviceID)
			if err != nil {
				return err
			}

			if held {
				return errs.New(errs.CodeDeviceLeased, "device %s is leased; release lease %s first", deviceID, lease.ID).
					WithDetail("lease_id", lease.ID)
			}
		}

		if err := tx.SetBorrowable(ctx, deviceID, !undo, p.now()); err != nil {
			return err
		}

		found.Borrowable = !undo
		device = found

		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}

	return ImportResult{Device: device}, nil
}

// findByName looks a device up by the name simpool gave it, which is how an
// interrupted create is recognised.
func (p *Pool) findByName(ctx context.Context, name string) (provider.Device, bool, error) {
	devices, err := p.provider.ListDevices(ctx)
	if err != nil {
		return provider.Device{}, false, err
	}

	for _, device := range devices {
		if device.Name == name {
			return device, true, nil
		}
	}

	return provider.Device{}, false, nil
}
