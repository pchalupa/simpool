package pool_test

import (
	"context"
	"errors"
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

func newHarness(t *testing.T, maxDevices int) *harness {
	t.Helper()

	platform := fake.New()
	testClock := newClock()

	// t.TempDir lives behind a symlink on macOS; canonicalise it so worktree
	// comparisons match what simpool records.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}

	return &harness{
		pool:     openPool(t, dir, platform, testClock, maxDevices),
		platform: platform,
		clock:    testClock,
		dir:      dir,
	}
}

// openPool builds an independent pool over the same database, standing in for a
// separate simpool process.
func openPool(t *testing.T, dir string, platform provider.Provider, testClock *clock, maxDevices int) *pool.Pool {
	t.Helper()

	cfg := config.Config{
		StateDir:     dir,
		DatabasePath: filepath.Join(dir, "pool.db"),
		MaxDevices:   maxDevices,
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

// openStore opens the pool database directly, which tests use to plant the
// records a crashed process would have left behind.
func openStore(t *testing.T, dir string) *store.Store {
	t.Helper()

	state, err := store.Open(context.Background(), filepath.Join(dir, "pool.db"))
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	t.Cleanup(func() { state.Close() })

	return state
}

// acquireWithCreate is the common "give me a device, make one if you must"
// request.
func acquireWithCreate() pool.AcquireRequest {
	return pool.AcquireRequest{
		CreateIfNeeded: true,
		Boot:           true,
	}
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

func TestExternalDevicesNeedAnExplicitImport(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)
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

	if result.Created {
		t.Fatal("an imported device must not be reported as created")
	}
}

func TestAcquireCreatesWithinCapacity(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 1)

	first, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	if !first.Created {
		t.Fatal("expected the first acquire to create a device")
	}

	_, err = h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
	requireCode(t, err, errs.CodeNoCapacity)

	if _, err := h.pool.Release(ctx, first.Lease.ID, false); err != nil {
		t.Fatalf("release: %v", err)
	}

	second, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	if second.Created {
		t.Fatal("expected the released device to be reused, not a new one")
	}

	if second.Device.ID != first.Device.ID {
		t.Fatalf("expected to reuse %s, got %s", first.Device.ID, second.Device.ID)
	}
}

func TestConcurrentAcquiresNeverShareADevice(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	for range 2 {
		device := h.platform.AddExisting("shared")

		if _, err := h.pool.Import(ctx, device.ID, false); err != nil {
			t.Fatalf("importing device: %v", err)
		}
	}

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
			contender := openPool(t, h.dir, h.platform, h.clock, 2)

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
	h := newHarness(t, 2)

	first, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		CreateIfNeeded: true,
		TTL:            30 * time.Minute,
		Boot:           true,
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

	view, err := h.pool.Inspect(ctx, second.Device.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	if view.Lease == nil || view.Lease.ID != second.Lease.ID {
		t.Fatalf("the new borrower's lease was disturbed: %+v", view.Lease)
	}
}

func TestReleaseIsIdempotentAndUnknownLeaseIsDistinct(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
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
	h := newHarness(t, 2)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
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

func TestMetroPortConflictIsRejected(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 3)

	if _, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		CreateIfNeeded: true,
		Metadata: map[string]string{
			store.MetaMetroPort: "8081",
			store.MetaWorktree:  h.dir,
		},
		Boot: true,
	}); err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	_, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		CreateIfNeeded: true,
		Metadata:       map[string]string{store.MetaMetroPort: "8081"},
		Boot:           true,
	})
	requireCode(t, err, errs.CodeMetroPortConflict)

	if _, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		CreateIfNeeded: true,
		Metadata:       map[string]string{store.MetaMetroPort: "8082"},
		Boot:           true,
	}); err != nil {
		t.Fatalf("second acquire on a free port: %v", err)
	}
}

func TestLeaseMetadataIsVisibleEverywhere(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		CreateIfNeeded: true,
		Metadata: map[string]string{
			store.MetaWorktree:  h.dir,
			store.MetaMetroPort: "8091",
			"branch":            "feature/login",
		},
		Boot: true,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if acquired.Lease.Meta(store.MetaWorktree) != h.dir || acquired.Lease.Meta("branch") != "feature/login" {
		t.Fatalf("acquire response lost lease metadata: %+v", acquired.Lease)
	}

	view, err := h.pool.Inspect(ctx, acquired.Device.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	if view.Lease.Meta(store.MetaWorktree) != h.dir || view.Lease.Meta(store.MetaMetroPort) != "8091" {
		t.Fatalf("inspect lost lease metadata: %+v", view.Lease)
	}

	views, err := h.pool.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(views) != 1 || views[0].Lease == nil || views[0].Lease.Meta(store.MetaMetroPort) != "8091" {
		t.Fatalf("list lost lease metadata: %+v", views)
	}

	// Renewing merges: the Metro port moves, the other keys stay put.
	renewed, err := h.pool.Renew(ctx, pool.RenewRequest{
		LeaseID:  acquired.Lease.ID,
		Metadata: map[string]string{store.MetaMetroPort: "8092"},
	})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}

	if renewed.Lease.Meta(store.MetaMetroPort) != "8092" {
		t.Fatalf("renew did not update the Metro port: %+v", renewed.Lease)
	}

	if renewed.Lease.Meta("branch") != "feature/login" {
		t.Fatalf("renew dropped an unrelated metadata key: %+v", renewed.Lease)
	}

	// An empty value removes a key.
	cleared, err := h.pool.Renew(ctx, pool.RenewRequest{
		LeaseID:  acquired.Lease.ID,
		Metadata: map[string]string{store.MetaMetroPort: ""},
	})
	if err != nil {
		t.Fatalf("renew clearing the port: %v", err)
	}

	if _, present := cleared.Lease.Metadata[store.MetaMetroPort]; present {
		t.Fatalf("expected the Metro port to be removed: %+v", cleared.Lease)
	}
}

// TestLeasesWithoutMetadataAreNormal covers the common case: a simulator
// borrowed with no worktree, no Metro server and no keys at all.
func TestLeasesWithoutMetadataAreNormal(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	first, err := h.pool.Acquire(ctx, acquireWithCreate())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if len(first.Lease.Metadata) != 0 {
		t.Fatalf("expected an empty metadata bag, got %+v", first.Lease.Metadata)
	}

	// Two bare leases must not collide on an absent Metro port.
	second, err := h.pool.Acquire(ctx, acquireWithCreate())
	if err != nil {
		t.Fatalf("second bare acquire: %v", err)
	}

	if second.Device.ID == first.Device.ID {
		t.Fatal("expected a second device")
	}

	view, err := h.pool.Inspect(ctx, first.Device.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	if len(view.Lease.Metadata) != 0 {
		t.Fatalf("expected an empty metadata bag after a round trip, got %+v", view.Lease.Metadata)
	}
}

func TestMetadataIsValidated(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	cases := map[string]map[string]string{
		"bad port":  {store.MetaMetroPort: "not-a-port"},
		"port zero": {store.MetaMetroPort: "0"},
		"bad key":   {"not a key!": "value"},
	}

	for name, metadata := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := h.pool.Acquire(ctx, pool.AcquireRequest{
				CreateIfNeeded: true,
				Metadata:       metadata,
				Boot:           true,
			})
			requireCode(t, err, errs.CodeInvalidArgs)
		})
	}

	// A dashed key is normalised to the underscored spelling simpool reads.
	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		CreateIfNeeded: true,
		Metadata:       map[string]string{"metro-port": "8081"},
		Boot:           true,
	})
	if err != nil {
		t.Fatalf("acquire with a dashed key: %v", err)
	}

	if acquired.Lease.Meta(store.MetaMetroPort) != "8081" {
		t.Fatalf("expected the key to normalise to %s: %+v", store.MetaMetroPort, acquired.Lease.Metadata)
	}
}

func TestDeleteProtectsExternalAndLeasedDevices(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	external := h.platform.AddExisting("personal iPhone")

	if _, err := h.pool.Import(ctx, external.ID, false); err != nil {
		t.Fatalf("import: %v", err)
	}

	_, err = h.pool.Delete(ctx, external.ID, false)
	requireCode(t, err, errs.CodeDeviceUnmanaged)

	// Even --force must not delete a device simpool did not create.
	_, err = h.pool.Delete(ctx, external.ID, true)
	requireCode(t, err, errs.CodeDeviceUnmanaged)

	_, err = h.pool.Delete(ctx, acquired.Device.ID, false)
	requireCode(t, err, errs.CodeDeviceLeased)

	if _, err := h.pool.Delete(ctx, acquired.Device.ID, true); err != nil {
		t.Fatalf("forced delete: %v", err)
	}

	if _, err := h.pool.Inspect(ctx, acquired.Device.ID); errs.CodeOf(err) != errs.CodeDeviceNotFound {
		t.Fatalf("expected the device to be gone, got %v", err)
	}
}

func TestDeviceRemovedOutsideSimpoolIsReportedNotHandedOut(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	h.platform.Remove(acquired.Device.ID)

	view, err := h.pool.Inspect(ctx, acquired.Device.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
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

func TestInterruptedCreateIsAdoptedNotLeaked(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	// The platform makes the device, then the call fails: the classic killed
	// process. The device must end up owned by the pool, not orphaned.
	h.platform.AfterCreate = func(provider.Device) error {
		h.platform.AfterCreate = nil

		return errors.New("simulated interruption")
	}

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
	if err != nil {
		t.Fatalf("acquire after an interrupted create: %v", err)
	}

	if h.platform.Count() != 1 {
		t.Fatalf("expected exactly one device on the platform, got %d", h.platform.Count())
	}

	view, err := h.pool.Inspect(ctx, acquired.Device.ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	if !view.Device.Managed() {
		t.Fatal("an adopted device must be owned by the pool")
	}
}

func TestBootFailureDoesNotStrandTheDevice(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	h.platform.BootErr = errs.New(errs.CodeProvider, "boot refused")

	_, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
	requireCode(t, err, errs.CodeProvider)

	// The device exists and is free again, so the next acquire reuses it.
	next, err := h.pool.Acquire(ctx, pool.AcquireRequest{Boot: true})
	if err != nil {
		t.Fatalf("acquire after a failed boot: %v", err)
	}

	if next.Created {
		t.Fatal("expected the device from the failed boot to be reused")
	}
}

func TestReclaimNeedsForceForALiveLease(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{CreateIfNeeded: true, Boot: true})
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

func TestDoctorReportsAnUnusablePool(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	report, err := h.pool.Doctor(ctx)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}

	if !report.Healthy {
		t.Fatalf("expected a healthy pool, got %+v", report.Checks)
	}

	if report.SchemaVersion != store.SchemaVersion {
		t.Fatalf("schema version %d, expected %d", report.SchemaVersion, store.SchemaVersion)
	}
}

func TestStateSurvivesSeparateProcesses(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	acquired, err := h.pool.Acquire(ctx, pool.AcquireRequest{
		CreateIfNeeded: true,
		Metadata:       map[string]string{store.MetaMetroPort: "8081"},
		Boot:           true,
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// A second process sees the lease the first one took.
	next := openPool(t, h.dir, h.platform, h.clock, 2)

	view, err := next.Inspect(ctx, acquired.Device.ID)
	if err != nil {
		t.Fatalf("inspect from a second process: %v", err)
	}

	if view.Lease == nil || view.Lease.ID != acquired.Lease.ID || view.Lease.Meta(store.MetaMetroPort) != "8081" {
		t.Fatalf("second process lost the lease: %+v", view.Lease)
	}

	if _, err := next.Release(ctx, acquired.Lease.ID, false); err != nil {
		t.Fatalf("release from a second process: %v", err)
	}
}
