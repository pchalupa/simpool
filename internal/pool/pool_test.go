package pool_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pchalupa/simpool/internal/config"
	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/pool"
	"github.com/pchalupa/simpool/internal/provider"
	"github.com/pchalupa/simpool/internal/provider/fake"
	"github.com/pchalupa/simpool/internal/store"
)

// clock is a test time source that only moves when a test moves it.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock {
	return &clock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(delta)
}

type harness struct {
	pool     *pool.Pool
	platform *fake.Provider
	clock    *clock
	dir      string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	platform := fake.New()
	testClock := newClock()
	dir := t.TempDir()

	return &harness{
		pool:     openPool(t, dir, platform, testClock),
		platform: platform,
		clock:    testClock,
		dir:      dir,
	}
}

// openPool builds an independent pool over the same database, standing in for a
// separate simpool process.
func openPool(t *testing.T, dir string, platform provider.Provider, testClock *clock) *pool.Pool {
	t.Helper()

	cfg := config.Config{
		StateDir:     dir,
		DatabasePath: filepath.Join(dir, "pool.db"),
		LeaseTTL:     time.Hour,
		OpTimeout:    time.Minute,
		Borrower:     "test",
	}

	state, err := store.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	t.Cleanup(func() { state.Close() })

	instance := pool.New(cfg, state, platform)
	instance.SetClock(testClock.Now)

	return instance
}

// addDevices puts simulators on the fake host and imports them, which is how a
// device becomes borrowable.
func (h *harness) addDevices(t *testing.T, count int) []provider.Device {
	t.Helper()

	devices := make([]provider.Device, 0, count)

	for range count {
		device := h.platform.AddExisting("pool device")

		if _, err := h.pool.Import(context.Background(), device.ID, false); err != nil {
			t.Fatalf("importing device: %v", err)
		}

		devices = append(devices, device)
	}

	return devices
}

// findDevice looks one device up in the reconciled pool listing.
func findDevice(t *testing.T, instance *pool.Pool, deviceID string) (pool.DeviceView, bool) {
	t.Helper()

	views, err := instance.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	for _, view := range views {
		if view.Device.ID == deviceID {
			return view, true
		}
	}

	return pool.DeviceView{}, false
}

func requireCode(t *testing.T, err error, want errs.Code) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error with code %q, got nil", want)
	}

	if got := errs.CodeOf(err); got != want {
		t.Fatalf("expected error code %q, got %q (%v)", want, got, err)
	}
}

func TestDevicesNeedAnExplicitImport(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	existing := h.platform.AddExisting("Someone's iPhone")

	_, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	requireCode(t, err, errs.CodeNoDeviceAvailable)

	if _, err := h.pool.Import(ctx, existing.ID, false); err != nil {
		t.Fatalf("importing device: %v", err)
	}

	result, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("acquiring imported device: %v", err)
	}

	if result.Device.ID != existing.ID {
		t.Fatalf("expected device %s, got %s", existing.ID, result.Device.ID)
	}

	if !result.Booted {
		t.Fatal("expected the acquired device to be booted")
	}
}

func TestReleasedDeviceIsHandedOutAgain(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	first, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	_, err = h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	requireCode(t, err, errs.CodeNoDeviceAvailable)

	if _, err := h.pool.Release(ctx, first.Lease.ID, false); err != nil {
		t.Fatalf("release: %v", err)
	}

	second, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	if second.Device.ID != first.Device.ID {
		t.Fatalf("expected to reuse %s, got %s", first.Device.ID, second.Device.ID)
	}
}

func TestConcurrentAcquiresNeverShareADevice(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 2)

	const contenders = 8

	var (
		wait     sync.WaitGroup
		mu       sync.Mutex
		acquired = map[string]string{}
		failures = []error{}
	)

	for range contenders {
		wait.Add(1)

		go func() {
			defer wait.Done()

			// Each contender opens its own database handle, as a separate
			// simpool process would.
			contender := openPool(t, h.dir, h.platform, h.clock)

			result, err := contender.Acquire(ctx, pool.AcquireRequest{Boot: true})

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				failures = append(failures, err)

				return
			}

			if previous, taken := acquired[result.Device.ID]; taken {
				t.Errorf("device %s handed to both lease %s and %s", result.Device.ID, previous, result.Lease.ID)
			}

			acquired[result.Device.ID] = result.Lease.ID
		}()
	}

	wait.Wait()

	if len(acquired) != 2 {
		t.Fatalf("expected exactly 2 winners, got %d", len(acquired))
	}

	if len(failures) != contenders-2 {
		t.Fatalf("expected %d losers, got %d", contenders-2, len(failures))
	}

	for _, err := range failures {
		requireCode(t, err, errs.CodeNoDeviceAvailable)
	}
}

func TestExpiredLeaseIsReclaimedAndItsIDGoesInert(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	first, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		TTL:  30 * time.Minute,
		Boot: true,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	h.clock.Advance(31 * time.Minute)

	_, err = h.pool.Renew(ctx, pool.RenewRequest{LeaseID: first.Lease.ID})
	requireCode(t, err, errs.CodeLeaseNotLive)

	second, err := h.pool.Acquire(ctx, pool.AcquireRequest{Borrower: "other", Boot: true})
	if err != nil {
		t.Fatalf("second borrower could not take the expired device: %v", err)
	}

	if second.Device.ID != first.Device.ID {
		t.Fatalf("expected the expired device %s to be reused, got %s", first.Device.ID, second.Device.ID)
	}

	// The stale lease ID must not touch the new borrower's lease.
	release, err := h.pool.Release(ctx, first.Lease.ID, false)
	if err != nil {
		t.Fatalf("releasing the stale lease: %v", err)
	}

	if !release.AlreadyReleased {
		t.Fatal("expected the stale lease to report itself already closed")
	}

	view, found := findDevice(t, h.pool, second.Device.ID)
	if !found {
		t.Fatalf("device %s is not in the pool", second.Device.ID)
	}

	if view.Lease == nil || view.Lease.ID != second.Lease.ID {
		t.Fatalf("the new borrower's lease was disturbed: %+v", view.Lease)
	}
}

func TestReleaseIsIdempotentAndUnknownLeaseIsDistinct(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	first, err := h.pool.Release(ctx, acquired.Lease.ID, false)
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	if !first.Released || first.AlreadyReleased {
		t.Fatalf("expected a fresh release, got %+v", first)
	}

	second, err := h.pool.Release(ctx, acquired.Lease.ID, false)
	if err != nil {
		t.Fatalf("second release: %v", err)
	}

	if !second.AlreadyReleased {
		t.Fatalf("expected the second release to be idempotent, got %+v", second)
	}

	_, err = h.pool.Release(ctx, "lease_does_not_exist", false)
	requireCode(t, err, errs.CodeLeaseNotFound)

	if h.platform.StateOf(acquired.Device.ID) != provider.StateBooted {
		t.Fatal("release must leave the device booted by default")
	}
}

func TestReleaseCanShutDownTheDevice(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if _, err := h.pool.Release(ctx, acquired.Lease.ID, true); err != nil {
		t.Fatalf("release --shutdown: %v", err)
	}

	if h.platform.StateOf(acquired.Device.ID) != provider.StateShutdown {
		t.Fatal("expected the device to be shut down")
	}

	if h.platform.Count() != 1 {
		t.Fatal("release must never delete the device")
	}
}

func TestLeaseMetadataIsVisibleEverywhere(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		Metadata: map[string]string{
			"worktree": "/checkouts/app",
			"port":     "8091",
			"branch":   "feature/login",
		},
		Boot: true,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if acquired.Lease.Metadata["worktree"] != "/checkouts/app" || acquired.Lease.Metadata["branch"] != "feature/login" {
		t.Fatalf("acquire response lost lease metadata: %+v", acquired.Lease)
	}

	view, found := findDevice(t, h.pool, acquired.Device.ID)
	if !found {
		t.Fatalf("device %s is not in the pool", acquired.Device.ID)
	}

	if view.Lease == nil || view.Lease.Metadata["port"] != "8091" {
		t.Fatalf("list lost lease metadata: %+v", view.Lease)
	}

	// Renewing merges: the updated key changes, the other keys stay put.
	renewed, err := h.pool.Renew(ctx, pool.RenewRequest{
		LeaseID:  acquired.Lease.ID,
		Metadata: map[string]string{"port": "8092"},
	})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}

	if renewed.Lease.Metadata["port"] != "8092" {
		t.Fatalf("renew did not update the key: %+v", renewed.Lease)
	}

	if renewed.Lease.Metadata["branch"] != "feature/login" {
		t.Fatalf("renew dropped an unrelated metadata key: %+v", renewed.Lease)
	}

	// An empty value removes a key.
	cleared, err := h.pool.Renew(ctx, pool.RenewRequest{
		LeaseID:  acquired.Lease.ID,
		Metadata: map[string]string{"port": ""},
	})
	if err != nil {
		t.Fatalf("renew clearing the key: %v", err)
	}

	if _, present := cleared.Lease.Metadata["port"]; present {
		t.Fatalf("expected the key to be removed: %+v", cleared.Lease)
	}
}

// TestMetadataHasNoSpecialKeys checks that simpool stores every value as given
// and never treats two leases with the same value as a conflict.
func TestMetadataHasNoSpecialKeys(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 2)

	metadata := map[string]string{
		"worktree":   "relative/checkout",
		"metro_port": "08081",
	}

	first, err := h.pool.Acquire(ctx, pool.AcquireRequest{Metadata: metadata, Boot: true})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	second, err := h.pool.Acquire(ctx, pool.AcquireRequest{Metadata: metadata, Boot: true})
	if err != nil {
		t.Fatalf("second acquire with the same metadata: %v", err)
	}

	for _, lease := range []store.Lease{first.Lease, second.Lease} {
		if lease.Metadata["worktree"] != "relative/checkout" || lease.Metadata["metro_port"] != "08081" {
			t.Fatalf("metadata was rewritten: %+v", lease.Metadata)
		}
	}
}

// TestLeasesWithoutMetadataAreNormal covers the common case: a simulator
// borrowed with no metadata at all.
func TestLeasesWithoutMetadataAreNormal(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if len(acquired.Lease.Metadata) != 0 {
		t.Fatalf("expected an empty metadata bag, got %+v", acquired.Lease.Metadata)
	}

	view, found := findDevice(t, h.pool, acquired.Device.ID)
	if !found {
		t.Fatalf("device %s is not in the pool", acquired.Device.ID)
	}

	if len(view.Lease.Metadata) != 0 {
		t.Fatalf("expected an empty metadata bag after a round trip, got %+v", view.Lease.Metadata)
	}
}

func TestMetadataIsValidated(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	_, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		Metadata: map[string]string{"not a key!": "value"},
		Boot:     true,
	})
	requireCode(t, err, errs.CodeInvalidArgs)

	// A dashed key is normalised to the underscored spelling.
	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		Metadata: map[string]string{"my-key": "value"},
		Boot:     true,
	})
	if err != nil {
		t.Fatalf("acquire with a dashed key: %v", err)
	}

	if acquired.Lease.Metadata["my_key"] != "value" {
		t.Fatalf("expected the key to normalise to my_key: %+v", acquired.Lease.Metadata)
	}
}

func TestDeviceRemovedOutsideSimpoolIsReportedNotHandedOut(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	h.platform.Remove(acquired.Device.ID)

	view, found := findDevice(t, h.pool, acquired.Device.ID)
	if !found {
		t.Fatalf("device %s is not in the pool", acquired.Device.ID)
	}

	if !view.Device.Missing() {
		t.Fatal("expected the vanished device to be marked missing")
	}

	if view.Lease != nil {
		t.Fatal("a missing device must not keep a live lease")
	}

	_, err = h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	requireCode(t, err, errs.CodeNoDeviceAvailable)

	_, err = h.pool.Renew(ctx, pool.RenewRequest{LeaseID: acquired.Lease.ID})
	requireCode(t, err, errs.CodeLeaseNotLive)
}

func TestBootFailureDoesNotStrandTheDevice(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	device := h.addDevices(t, 1)[0]

	h.platform.BootErr = errs.New(errs.CodeProvider, "boot refused")

	_, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	requireCode(t, err, errs.CodeProvider)

	// The device is free again, so the next acquire gets it.
	next, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("acquire after a failed boot: %v", err)
	}

	if next.Device.ID != device.ID {
		t.Fatalf("expected device %s from the failed boot, got %s", device.ID, next.Device.ID)
	}
}

func TestReclaimNeedsForceForALiveLease(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	_, err = h.pool.Reclaim(ctx, acquired.Device.ID, false)
	requireCode(t, err, errs.CodeDeviceLeased)

	reclaimed, err := h.pool.Reclaim(ctx, acquired.Device.ID, true)
	if err != nil {
		t.Fatalf("forced reclaim: %v", err)
	}

	if reclaimed.ReclaimedLease != acquired.Lease.ID {
		t.Fatalf("expected lease %s to be reclaimed, got %q", acquired.Lease.ID, reclaimed.ReclaimedLease)
	}

	if _, err := h.pool.Acquire(ctx, pool.AcquireRequest{Borrower: "next", Boot: true}); err != nil {
		t.Fatalf("acquire after reclaim: %v", err)
	}
}

func TestStateSurvivesSeparateProcesses(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.addDevices(t, 1)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		Metadata: map[string]string{"task": "login-flow"},
		Boot:     true,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// A second process sees the lease the first one took.
	next := openPool(t, h.dir, h.platform, h.clock)

	view, found := findDevice(t, next, acquired.Device.ID)
	if !found {
		t.Fatalf("device %s is not in the pool", acquired.Device.ID)
	}

	if view.Lease == nil || view.Lease.ID != acquired.Lease.ID || view.Lease.Metadata["task"] != "login-flow" {
		t.Fatalf("second process lost the lease: %+v", view.Lease)
	}

	if _, err := next.Release(ctx, acquired.Lease.ID, false); err != nil {
		t.Fatalf("release from a second process: %v", err)
	}
}
