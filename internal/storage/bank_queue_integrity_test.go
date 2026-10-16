package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A queued retain's write is told to leave the operation queue alone because
// the worker already owns the child's operation record. The write must not
// mistake that record for a reused identifier: the child is *this* write, so
// finding it in the queue is expected, not a conflict. A regression here makes
// every queued retain fail with "operation id already used".
func TestRetainWriteSkipOperationRecordToleratesOwnQueuedOperation(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "child-1", BankID: "b1", OperationType: AsyncRetainType,
		Status: StatusProcessing, WorkerID: "worker-1", Run: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID:              "b1",
		OperationID:         "child-1",
		SkipOperationRecord: true,
		Items:               []RetainItem{{Content: "Alice moved to Berlin."}},
	})
	if err != nil {
		t.Fatalf("a queued write must accept its own operation record: %v", err)
	}
	if len(outcome.ItemUnitIDs) != 1 || len(outcome.ItemUnitIDs[0]) == 0 {
		t.Fatalf("outcome = %+v, want one unit written", outcome)
	}
}

// Deleting a bank removes the work it had queued, so a worker cannot pick up an
// operation whose bank no longer exists. The postgres backend gets this from the
// async_operations foreign-key cascade; the memory backend must purge the queue
// itself, or the row survives and a worker resurrects the bank.
func TestDeleteBankPurgesQueuedOperations(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "op-1", BankID: "b1", OperationType: AsyncRetainType,
		Status: StatusPending, Run: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteBank(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOperation(ctx, "b1", "op-1"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("a deleted bank's queued operation must be gone, got %v", err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("a deleted bank's queued operation must not be claimable, got %v", err)
	}
}

// A consume-time liveness read reports a deleted or cancelled operation as dead
// and any live one as alive, which is what the worker's execution-start guard
// consults. The read is scoped to the bank, so another bank's row cannot be
// mistaken for this one's.
func TestOperationAliveReportsDeletionAndCancellation(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "op-1", BankID: "b1", OperationType: AsyncRetainType,
		Status: StatusProcessing, WorkerID: "worker-1", Run: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if alive, err := store.OperationAlive(ctx, "b1", "op-1"); err != nil || !alive {
		t.Fatalf("a live operation must read alive, got alive=%v err=%v", alive, err)
	}
	// Cancelling flips the row terminal without removing it.
	if _, err := store.CancelOperation(ctx, "b1", "op-1"); err != nil {
		t.Fatal(err)
	}
	if alive, err := store.OperationAlive(ctx, "b1", "op-1"); err != nil || alive {
		t.Fatalf("a cancelled operation must read dead, got alive=%v err=%v", alive, err)
	}
	// Deleting the bank removes the bank's operation rows, so a live operation
	// there reads dead once the bank is gone.
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "op-2", BankID: "b1", OperationType: AsyncRetainType,
		Status: StatusProcessing, WorkerID: "worker-1", Run: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteBank(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if alive, err := store.OperationAlive(ctx, "b1", "op-2"); err != nil || alive {
		t.Fatalf("a missing operation must read dead, got alive=%v err=%v", alive, err)
	}
}
