//go:build pgx

package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Deleting a terminal export operation removes the stored archive its
// result_metadata points at, so the blob does not outlive the row that is its
// only handle. This is decided by SQL on the postgres backend, so it is tested
// against a real database.
func TestPostgresDeleteOperationDropsExportArchive(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "del_archive")
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	const opID = "11111111-1111-1111-1111-111111111111"
	key := StoredFileKeyForBank("b1", "exports-"+opID+".zip")
	if err := pg.PutStoredFile(ctx, StoredFile{Key: key, Data: []byte("PK\x03\x04archive")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutOperation(ctx, Operation{
		OperationID: opID, BankID: "b1", OperationType: OperationTypeTransferExport, Status: StatusCompleted,
		ResultMetadata: map[string]any{"storage_key": key, "download_url": "/v1/default/files/download/" + key},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.GetStoredFile(ctx, key); err != nil {
		t.Fatalf("archive must exist before delete: %v", err)
	}
	if _, err := pg.DeleteOperation(ctx, "b1", opID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := pg.GetOperation(ctx, "b1", opID); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("deleted operation still readable: %v", err)
	}
	if _, err := pg.GetStoredFile(ctx, key); !errors.Is(err, ErrStoredFileNotFound) {
		t.Fatalf("archive survived the delete: err = %v", err)
	}
}

// The retention prune removes an expired export operation's stored archive
// together with the row, so the blob does not outlive its only handle, and it
// leaves an export that has not reached its window alone.
func TestPostgresPruneDropsExportArchive(t *testing.T) {
	ctx := context.Background()
	pg, schema := newClearMemoriesStore(t, "prune_archive")
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	const opID = "22222222-2222-2222-2222-222222222222"
	key := StoredFileKeyForBank("b1", "exports-"+opID+".zip")
	if err := pg.PutStoredFile(ctx, StoredFile{Key: key, Data: []byte("PK\x03\x04archive")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutOperation(ctx, Operation{
		OperationID: opID, BankID: "b1", OperationType: OperationTypeTransferExport, Status: StatusCompleted,
		ResultMetadata: map[string]any{"storage_key": key},
	}); err != nil {
		t.Fatal(err)
	}
	// A cutoff before the row's age prunes nothing: the archive stays put.
	if n, err := pg.PruneTerminalOperations(ctx, schema, time.Now().Add(-time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("prune before expiry: n=%d err=%v", n, err)
	}
	if _, err := pg.GetStoredFile(ctx, key); err != nil {
		t.Fatalf("archive removed before expiry: %v", err)
	}
	// A cutoff after it prunes the row and its archive in step.
	if n, err := pg.PruneTerminalOperations(ctx, schema, time.Now().Add(time.Hour), 100); err != nil || n != 1 {
		t.Fatalf("prune after expiry: n=%d err=%v", n, err)
	}
	if _, err := pg.GetOperation(ctx, "b1", opID); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("operation row survived the prune: %v", err)
	}
	if _, err := pg.GetStoredFile(ctx, key); !errors.Is(err, ErrStoredFileNotFound) {
		t.Fatalf("archive survived the prune: %v", err)
	}
}

// RepairPendingBatchParent persists the corrected terminal status and stamps
// completed_at, but only for a row still "pending": a row already terminal (or
// still running) is left untouched.
func TestPostgresRepairPendingBatchParent(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "repair_parent")
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	const parent = "11111111-1111-1111-1111-111111111111"
	const running = "22222222-2222-2222-2222-222222222222"
	for _, op := range []Operation{
		{OperationID: parent, BankID: "b1", OperationType: AsyncRetainType, Status: StatusPending,
			ResultMetadata: map[string]any{"is_parent": true}},
		{OperationID: running, BankID: "b1", OperationType: AsyncRetainType, Status: StatusProcessing,
			ResultMetadata: map[string]any{"is_parent": true}},
	} {
		if _, err := pg.PutOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.RepairPendingBatchParent(ctx, "b1", parent, StatusCompleted); err != nil {
		t.Fatal(err)
	}
	got, err := pg.GetOperation(ctx, "b1", parent)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusCompleted || got.CompletedAt == nil {
		t.Fatalf("pending parent not repaired: %+v", got)
	}
	// The guard is on the stored status: a row that is not pending is untouched.
	if err := pg.RepairPendingBatchParent(ctx, "b1", running, StatusCompleted); err != nil {
		t.Fatal(err)
	}
	runningGot, err := pg.GetOperation(ctx, "b1", running)
	if err != nil {
		t.Fatal(err)
	}
	if runningGot.Status != StatusProcessing {
		t.Fatalf("a non-pending parent must not be repaired: %+v", runningGot)
	}
}
