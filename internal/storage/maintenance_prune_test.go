package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The in-memory retention prune removes an expired export operation's stored
// archive with the row, so the blob does not outlive its only handle. It also
// honours the window: an export whose row is not yet old enough keeps both its
// row and its archive, so the configured retention is what decides.
func TestMemoryPruneTerminalOperationsDropsExportArchive(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	key := StoredFileKeyForBank("b1", "exports-1.zip")
	if err := store.PutStoredFile(ctx, StoredFile{Key: key, Data: []byte("PK\x03\x04archive")}); err != nil {
		t.Fatal(err)
	}
	const opID = "11111111-1111-1111-1111-111111111111"
	if _, err := store.PutOperation(ctx, Operation{
		OperationID:    opID,
		BankID:         "b1",
		OperationType:  OperationTypeTransferExport,
		Status:         StatusCompleted,
		ResultMetadata: map[string]any{"storage_key": key},
	}); err != nil {
		t.Fatal(err)
	}

	// A cutoff in the past leaves the fresh row and its blob alone.
	if n, err := store.PruneTerminalOperations(ctx, "public", time.Now().Add(-time.Hour), 100); err != nil || n != 0 {
		t.Fatalf("prune before expiry: n=%d err=%v", n, err)
	}
	if _, err := store.GetOperation(ctx, "b1", opID); err != nil {
		t.Fatalf("row pruned before expiry: %v", err)
	}
	if _, err := store.GetStoredFile(ctx, key); err != nil {
		t.Fatalf("archive removed before expiry: %v", err)
	}

	// A cutoff after the row's age prunes the row and its archive together.
	if n, err := store.PruneTerminalOperations(ctx, "public", time.Now().Add(time.Hour), 100); err != nil || n != 1 {
		t.Fatalf("prune after expiry: n=%d err=%v", n, err)
	}
	if _, err := store.GetOperation(ctx, "b1", opID); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("operation row survived the prune: %v", err)
	}
	if _, err := store.GetStoredFile(ctx, key); !errors.Is(err, ErrStoredFileNotFound) {
		t.Fatalf("archive survived the prune: %v", err)
	}
}
