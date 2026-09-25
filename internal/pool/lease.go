package pool

import (
	"context"
	"time"

	"github.com/strv/simpool/internal/errs"
	"github.com/strv/simpool/internal/provider"
	"github.com/strv/simpool/internal/store"
)

// RenewRequest extends a lease and may update its metadata, for instance when
// Metro is started after the device was acquired. Metadata is merged into what
// the lease already carries; a key given with an empty value is removed.
type RenewRequest struct {
	LeaseID  string
	TTL      time.Duration
	Metadata map[string]string
}

// RenewResult carries the extended lease and its device.
type RenewResult struct {
	Device store.Device `json:"device"`
	Lease  store.Lease  `json:"lease"`
}

// Renew extends a live lease. An expired or released lease is never revived:
// its borrower must acquire again, so an old lease ID cannot disturb whoever
// holds the device now.
func (p *Pool) Renew(ctx context.Context, request RenewRequest) (RenewResult, error) {
	if err := p.ExpireLeases(ctx); err != nil {
		return RenewResult{}, err
	}

	if request.TTL <= 0 {
		request.TTL = p.cfg.LeaseTTL
	}

	update, err := normalizeMetadata(request.Metadata)
	if err != nil {
		return RenewResult{}, err
	}

	var result RenewResult

	now := p.now()

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		lease, err := tx.GetLease(ctx, request.LeaseID)
		if err != nil {
			return err
		}

		renewed, err := tx.RenewLease(ctx, request.LeaseID, now.Add(request.TTL), now)
		if err != nil {
			return err
		}

		if !renewed {
			return notLive(lease)
		}

		if len(update) > 0 {
			merged := mergeMetadata(lease.Metadata, update)

			if err := p.checkMetroPort(ctx, tx, merged[store.MetaMetroPort], lease.ID); err != nil {
				return err
			}

			if err := tx.SetLeaseMetadata(ctx, lease.ID, merged); err != nil {
				return err
			}
		}

		if result.Lease, err = tx.GetLease(ctx, request.LeaseID); err != nil {
			return err
		}

		result.Device, err = tx.GetDevice(ctx, lease.DeviceID)

		return err
	})
	if err != nil {
		return RenewResult{}, err
	}

	return result, nil
}

// ReleaseResult reports what a release did. Releasing an already released lease
// is not an error: it reports the original outcome instead.
type ReleaseResult struct {
	LeaseID         string `json:"lease_id"`
	DeviceID        string `json:"device_id"`
	Released        bool   `json:"released"`
	AlreadyReleased bool   `json:"already_released"`
	ReleaseReason   string `json:"release_reason"`
	ShutDown        bool   `json:"shut_down"`
}

// Release frees a lease. The device is left booted by default so the next
// borrower starts fast; pass shutdown to power it off. Devices are never
// deleted on release.
func (p *Pool) Release(ctx context.Context, leaseID string, shutdown bool) (ReleaseResult, error) {
	var result ReleaseResult

	now := p.now()

	err := p.store.Write(ctx, func(tx *store.Tx) error {
		lease, err := tx.GetLease(ctx, leaseID)
		if err != nil {
			return err
		}

		released, err := tx.ReleaseLease(ctx, leaseID, store.ReleaseReasonReleased, now)
		if err != nil {
			return err
		}

		result = ReleaseResult{
			LeaseID:         leaseID,
			DeviceID:        lease.DeviceID,
			Released:        released,
			AlreadyReleased: !released,
			ReleaseReason:   store.ReleaseReasonReleased,
		}

		if !released {
			result.ReleaseReason = lease.ReleaseReason
		}

		return nil
	})
	if err != nil {
		return ReleaseResult{}, err
	}

	if !shutdown {
		return result, nil
	}

	if err := p.provider.Shutdown(ctx, result.DeviceID); err != nil {
		return result, err
	}

	result.ShutDown = true

	err = p.store.Write(ctx, func(tx *store.Tx) error {
		return tx.SetDeviceState(ctx, result.DeviceID, string(provider.StateShutdown), p.now())
	})
	if err != nil {
		return ReleaseResult{}, err
	}

	return result, nil
}

// ReclaimResult reports an explicit recovery of a stuck device.
type ReclaimResult struct {
	DeviceID       string `json:"device_id"`
	ReclaimedLease string `json:"reclaimed_lease,omitempty"`
	AlreadyFree    bool   `json:"already_free"`
}

// Reclaim frees a device whose borrower is gone. An expired lease is reclaimed
// on sight; a live one needs force, so reclaim never silently steals a device
// from a working agent.
func (p *Pool) Reclaim(ctx context.Context, deviceID string, force bool) (ReclaimResult, error) {
	// Reconciling first turns merely expired leases into released ones.
	if _, err := p.Reconcile(ctx); err != nil {
		return ReclaimResult{}, err
	}

	result := ReclaimResult{DeviceID: deviceID}

	now := p.now()

	err := p.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.GetDevice(ctx, deviceID); err != nil {
			return err
		}

		lease, held, err := tx.LiveLeaseForDevice(ctx, deviceID)
		if err != nil {
			return err
		}

		if !held {
			result.AlreadyFree = true

			return nil
		}

		if !force {
			return errs.New(errs.CodeDeviceLeased, "lease %s on device %s is live until %s; pass --force to reclaim it anyway", lease.ID, deviceID, lease.ExpiresAt.Local().Format("2006-01-02 15:04")).
				WithDetail("lease_id", lease.ID).
				WithDetail("borrower", lease.Borrower).
				WithDetail("expires_at", lease.ExpiresAt)
		}

		if _, err := tx.ReleaseLease(ctx, lease.ID, store.ReleaseReasonReclaimed, now); err != nil {
			return err
		}

		result.ReclaimedLease = lease.ID

		return nil
	})
	if err != nil {
		return ReclaimResult{}, err
	}

	return result, nil
}

// notLive explains why a lease can no longer be renewed.
func notLive(lease store.Lease) error {
	reason := lease.ReleaseReason
	if reason == "" {
		reason = store.ReleaseReasonExpired
	}

	return errs.New(errs.CodeLeaseNotLive, "lease %s is no longer live (%s); acquire a new one", lease.ID, reason).
		WithDetail("device_id", lease.DeviceID).
		WithDetail("release_reason", reason).
		WithDetail("expires_at", lease.ExpiresAt)
}
