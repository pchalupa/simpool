package pool

import (
	"context"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/provider"
	"github.com/pchalupa/simpool/internal/store"
)

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

// Import opts an existing simulator into the pool, or (with undo) takes it back
// out. simpool never deletes a simulator either way.
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
