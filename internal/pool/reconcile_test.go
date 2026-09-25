package pool_test

import (
	"context"
	"testing"
	"time"

	"github.com/pchalupa/simpool/internal/store"
)

// TestReconcileAdoptsADeviceLeftByAKilledProcess covers the case the operation
// record exists for: simctl made the simulator, then the CLI died before it
// could record it.
func TestReconcileAdoptsADeviceLeftByAKilledProcess(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	operationID := "op_killed_process"
	deviceName := "simpool-" + operationID
	now := h.clock.Now()

	state := openStore(t, h.dir)

	err := state.Write(ctx, func(tx *store.Tx) error {
		return tx.InsertOperation(ctx, store.Operation{
			ID:         operationID,
			Kind:       store.OperationCreate,
			State:      store.OperationPending,
			DeviceName: deviceName,
			Borrower:   "killed",
			StartedAt:  now,
			ExpiresAt:  now.Add(time.Minute),
		})
	})
	if err != nil {
		t.Fatalf("recording the interrupted operation: %v", err)
	}

	orphan := h.platform.AddExisting(deviceName)

	report, err := h.pool.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(report.AdoptedDevices) != 1 || report.AdoptedDevices[0] != orphan.ID {
		t.Fatalf("expected %s to be adopted, got %+v", orphan.ID, report.AdoptedDevices)
	}

	view, found := findDevice(t, h.pool, orphan.ID)
	if !found {
		t.Fatalf("device %s is not in the pool", orphan.ID)
	}

	if !view.Device.Managed() || !view.Device.Borrowable {
		t.Fatalf("an adopted device must be pool-owned and borrowable: %+v", view.Device)
	}

	err = state.Read(ctx, func(tx *store.Tx) error {
		pending, err := tx.PendingOperations(ctx)
		if err != nil {
			return err
		}

		if len(pending) != 0 {
			t.Fatalf("expected the operation to be closed, got %+v", pending)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("reading operations: %v", err)
	}
}

// TestReconcileFailsAnOperationThatNeverProducedADevice keeps an abandoned
// reservation from occupying pool capacity forever.
func TestReconcileFailsAnOperationThatNeverProducedADevice(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, 2)

	now := h.clock.Now()
	state := openStore(t, h.dir)

	err := state.Write(ctx, func(tx *store.Tx) error {
		return tx.InsertOperation(ctx, store.Operation{
			ID:         "op_abandoned",
			Kind:       store.OperationCreate,
			State:      store.OperationPending,
			DeviceName: "simpool-op_abandoned",
			Borrower:   "killed",
			StartedAt:  now,
			ExpiresAt:  now.Add(time.Minute),
		})
	})
	if err != nil {
		t.Fatalf("recording the interrupted operation: %v", err)
	}

	h.clock.Advance(2 * time.Minute)

	report, err := h.pool.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(report.AbandonedOps) != 1 {
		t.Fatalf("expected one abandoned operation, got %+v", report.AbandonedOps)
	}

	// Capacity is free again, so the pool can still create devices.
	if _, err := h.pool.Acquire(ctx, acquireWithCreate()); err != nil {
		t.Fatalf("acquire after reconciliation: %v", err)
	}
}
