package store_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pchalupa/simpool/internal/errs"
	"github.com/pchalupa/simpool/internal/store"
)

func openTestStore(t *testing.T, path string) *store.Store {
	t.Helper()

	state, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}

	t.Cleanup(func() { state.Close() })

	return state
}

func TestMigrationsAreIdempotentAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pool.db")

	first := openTestStore(t, path)
	second := openTestStore(t, path)

	for _, state := range []*store.Store{first, second} {
		err := state.Read(ctx, func(tx *store.Tx) error {
			version, err := tx.AppliedSchemaVersion(ctx)
			if err != nil {
				return err
			}

			if version != store.SchemaVersion {
				t.Fatalf("schema version %d, expected %d", version, store.SchemaVersion)
			}

			return nil
		})
		if err != nil {
			t.Fatalf("reading schema version: %v", err)
		}
	}
}

func TestConcurrentFirstOpenAppliesMigrationsOnce(t *testing.T) {
	const processes = 8

	for run := 0; run < 10; run++ {
		path := filepath.Join(t.TempDir(), "pool.db")
		start := make(chan struct{})
		failures := make(chan error, processes)

		var wg sync.WaitGroup

		for range processes {
			wg.Add(1)

			go func() {
				defer wg.Done()
				<-start

				state, err := store.Open(context.Background(), path)
				if err != nil {
					failures <- err

					return
				}

				state.Close()
			}()
		}

		close(start)
		wg.Wait()
		close(failures)

		for err := range failures {
			t.Errorf("run %d: opening a fresh store concurrently: %v", run, err)
		}
	}
}

func TestOnlyOneLiveLeasePerDevice(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t, filepath.Join(t.TempDir(), "pool.db"))
	now := time.Now().UTC()

	err := state.Write(ctx, func(tx *store.Tx) error {
		return tx.ObserveDevice(ctx, store.Observation{
			ID:         "DEV-1",
			Platform:   "ios",
			Name:       "device",
			DeviceType: "type",
			Runtime:    "runtime",
			State:      "shutdown",
			Available:  true,
			Provenance: store.ProvenancePool,
			Borrowable: true,
		}, now)
	})
	if err != nil {
		t.Fatalf("observing device: %v", err)
	}

	lease := store.Lease{
		ID:         "lease-1",
		DeviceID:   "DEV-1",
		Borrower:   "first",
		AcquiredAt: now,
		ExpiresAt:  now.Add(time.Hour),
	}

	if err := state.Write(ctx, func(tx *store.Tx) error { return tx.InsertLease(ctx, lease) }); err != nil {
		t.Fatalf("inserting first lease: %v", err)
	}

	second := lease
	second.ID = "lease-2"
	second.Borrower = "second"

	err = state.Write(ctx, func(tx *store.Tx) error { return tx.InsertLease(ctx, second) })
	if code := errs.CodeOf(err); code != errs.CodeDeviceLeased {
		t.Fatalf("expected device_leased, got %q (%v)", code, err)
	}

	// Releasing the first lease frees the device for the next borrower.
	err = state.Write(ctx, func(tx *store.Tx) error {
		released, err := tx.ReleaseLease(ctx, lease.ID, store.ReleaseReasonReleased, now)
		if err != nil {
			return err
		}

		if !released {
			t.Fatal("expected the lease to be released")
		}

		return tx.InsertLease(ctx, second)
	})
	if err != nil {
		t.Fatalf("inserting the second lease after release: %v", err)
	}
}

func TestMetroPortIsUniqueAcrossLiveLeases(t *testing.T) {
	ctx := context.Background()
	state := openTestStore(t, filepath.Join(t.TempDir(), "pool.db"))
	now := time.Now().UTC()

	err := state.Write(ctx, func(tx *store.Tx) error {
		for _, id := range []string{"DEV-1", "DEV-2"} {
			observation := store.Observation{
				ID:         id,
				Platform:   "ios",
				Name:       id,
				DeviceType: "type",
				Runtime:    "runtime",
				State:      "shutdown",
				Available:  true,
				Provenance: store.ProvenancePool,
				Borrowable: true,
			}

			if err := tx.ObserveDevice(ctx, observation, now); err != nil {
				return err
			}
		}

		return tx.InsertLease(ctx, store.Lease{
			ID:         "lease-1",
			DeviceID:   "DEV-1",
			Borrower:   "first",
			Metadata:   map[string]string{store.MetaMetroPort: "8081"},
			AcquiredAt: now,
			ExpiresAt:  now.Add(time.Hour),
		})
	})
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}

	err = state.Write(ctx, func(tx *store.Tx) error {
		return tx.InsertLease(ctx, store.Lease{
			ID:         "lease-2",
			DeviceID:   "DEV-2",
			Borrower:   "second",
			Metadata:   map[string]string{store.MetaMetroPort: "8081"},
			AcquiredAt: now,
			ExpiresAt:  now.Add(time.Hour),
		})
	})
	if code := errs.CodeOf(err); code != errs.CodeMetroPortConflict {
		t.Fatalf("expected metro_port_conflict, got %q (%v)", code, err)
	}
}
