package pool

import (
	"context"
	"time"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/provider"
	"github.com/pchalupa/simpool/internal/store"
)

// AcquireRequest describes the device an agent wants to borrow. Metadata is
// free-form and optional.
type AcquireRequest struct {
	Platform   string
	DeviceType string
	Runtime    string
	Metadata   map[string]string
	Borrower   string
	TTL        time.Duration
	Boot       bool
}

// AcquireResult is what the borrower needs to start working.
type AcquireResult struct {
	Device store.Device `json:"device"`
	Lease  store.Lease  `json:"lease"`
	Booted bool         `json:"booted"`
}

// Acquire atomically reserves one matching device and boots it unless the
// caller opted out.
func (p *Pool) Acquire(ctx context.Context, request AcquireRequest) (AcquireResult, error) {
	if _, err := p.Reconcile(ctx); err != nil {
		return AcquireResult{}, err
	}

	request, err := p.normalizeAcquire(ctx, request)
	if err != nil {
		return AcquireResult{}, err
	}

	result, err := p.reserveExisting(ctx, request)
	if err != nil {
		return AcquireResult{}, err
	}

	if result == nil {
		return AcquireResult{}, p.noDeviceAvailable(ctx, request)
	}

	if !request.Boot {
		return *result, nil
	}

	if err := p.bootLeased(ctx, result); err != nil {
		return AcquireResult{}, err
	}

	return *result, nil
}

func (p *Pool) normalizeAcquire(ctx context.Context, request AcquireRequest) (AcquireRequest, error) {
	if request.Platform == "" {
		request.Platform = p.provider.Platform()
	}

	if request.Platform != p.provider.Platform() {
		return request, errs.New(errs.CodeInvalidArgs, "platform %q is not supported by this build", request.Platform)
	}

	if request.Borrower == "" {
		request.Borrower = p.cfg.Borrower
	}

	if request.TTL <= 0 {
		request.TTL = p.cfg.LeaseTTL
	}

	metadata, err := normalizeMetadata(request.Metadata)
	if err != nil {
		return request, err
	}

	request.Metadata = metadata

	if request.DeviceType, err = p.resolveDeviceType(ctx, request.DeviceType); err != nil {
		return request, err
	}

	if request.Runtime, err = p.resolveRuntime(ctx, request.Runtime); err != nil {
		return request, err
	}

	return request, nil
}

// reserveExisting takes the first matching idle device. It returns nil when the
// pool has nothing to hand out.
func (p *Pool) reserveExisting(ctx context.Context, request AcquireRequest) (*AcquireResult, error) {
	var result *AcquireResult

	now := p.now()

	err := p.store.Write(ctx, func(tx *store.Tx) error {
		device, found, err := tx.FindAvailableDevice(ctx, store.DeviceFilter{
			Platform:   request.Platform,
			DeviceType: request.DeviceType,
			Runtime:    request.Runtime,
		})
		if err != nil {
			return err
		}

		if !found {
			return nil
		}

		lease := newLease(request, device.ID, now)

		if err := tx.InsertLease(ctx, lease); err != nil {
			return err
		}

		result = &AcquireResult{
			Device: device,
			Lease:  lease,
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// bootLeased boots the leased device and waits for readiness. A boot failure
// releases the lease rather than stranding the device on a dead borrower.
func (p *Pool) bootLeased(ctx context.Context, result *AcquireResult) error {
	bootErr := p.provider.Boot(ctx, result.Device.ID)
	if bootErr == nil {
		bootErr = p.provider.WaitReady(ctx, result.Device.ID, p.cfg.OpTimeout)
	}

	if bootErr != nil {
		releaseErr := p.store.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.ReleaseLease(ctx, result.Lease.ID, store.ReleaseReasonReclaimed, p.now())

			return err
		})
		if releaseErr != nil {
			return releaseErr
		}

		return bootErr
	}

	result.Booted = true
	result.Device.State = string(provider.StateBooted)

	return p.store.Write(ctx, func(tx *store.Tx) error {
		return tx.SetDeviceState(ctx, result.Device.ID, string(provider.StateBooted), p.now())
	})
}

// noDeviceAvailable explains why nothing could be leased, distinguishing a pool
// with no borrowable devices from one that is merely busy.
func (p *Pool) noDeviceAvailable(ctx context.Context, request AcquireRequest) error {
	var (
		total      int
		borrowable int
	)

	err := p.store.Read(ctx, func(tx *store.Tx) error {
		devices, err := tx.ListDevices(ctx)
		if err != nil {
			return err
		}

		for _, device := range devices {
			if device.Platform != request.Platform || device.Missing() {
				continue
			}

			total++

			if device.Borrowable {
				borrowable++
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	failure := errs.New(errs.CodeNoDeviceAvailable, "no borrowable %s device matches the request", request.Platform).
		WithDetail("known_devices", total).
		WithDetail("borrowable_devices", borrowable)

	if borrowable == 0 && total > 0 {
		failure.WithDetail("hint", "simulators are not borrowable until `simpool import <device-id>`")
	}

	return failure
}

func newLease(request AcquireRequest, deviceID string, now time.Time) store.Lease {
	return store.Lease{
		ID:         newID("lease"),
		DeviceID:   deviceID,
		Borrower:   request.Borrower,
		Metadata:   request.Metadata,
		AcquiredAt: now,
		ExpiresAt:  now.Add(request.TTL),
	}
}
