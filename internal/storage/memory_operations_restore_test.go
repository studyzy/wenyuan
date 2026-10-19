package storage

import (
	"context"
	"testing"
	"time"
)

// A restored operation keeps the timestamps the archive carried, so an imported
// history reports when work ran rather than when it was imported. PutOperation
// is the wrong tool: it stamps the current time.
func TestRestoreOperationKeepsTheArchivedTimestamps(t *testing.T) {
	store := NewMemoryStore("b1")
	ctx := context.Background()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := created.Add(time.Hour)

	stored, err := store.RestoreOperation(ctx, Operation{
		OperationID: "op-1",
		BankID:      "b1",
		Status:      StatusCompleted,
		CreatedAt:   created,
		UpdatedAt:   updated,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %s, want the archived %s", stored.CreatedAt, created)
	}
	if !stored.UpdatedAt.Equal(updated) {
		t.Fatalf("updated_at = %s, want the archived %s", stored.UpdatedAt, updated)
	}
}

// An operation carrying no stamps still lands with usable ones rather than the
// zero time, which would read as 0001-01-01 everywhere it surfaces.
func TestRestoreOperationDefaultsMissingTimestamps(t *testing.T) {
	store := NewMemoryStore("b1")
	stored, err := store.RestoreOperation(context.Background(), Operation{
		OperationID: "op-2",
		BankID:      "b1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.CreatedAt.IsZero() {
		t.Fatal("created_at is the zero time")
	}
	if !stored.UpdatedAt.Equal(stored.CreatedAt) {
		t.Fatalf("updated_at = %s, want it to fall back to created_at %s", stored.UpdatedAt, stored.CreatedAt)
	}
}

// A pending scoped consolidation covers only its own tag subset, so it must not
// swallow a full-bank sweep. Scanning the operation map made the old check
// order-dependent: whichever outstanding pass the map yielded first won.
func TestFullBankConsolidationIsNotDeduplicatedIntoAScopedOne(t *testing.T) {
	store := NewMemoryStore("b1")
	ctx := context.Background()

	if _, deduped, err := store.EnqueueConsolidationScoped(ctx, "b1", [][]string{{"team:exec"}}); err != nil || deduped {
		t.Fatalf("scoped submit: deduped = %v, err = %v", deduped, err)
	}
	full, deduped, err := store.EnqueueConsolidation(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if deduped {
		t.Fatalf("the full-bank sweep was folded into the pending scoped pass: %+v", full)
	}
	// A second full-bank submit does deduplicate against the first one.
	again, deduped, err := store.EnqueueConsolidation(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if !deduped || again.OperationID != full.OperationID {
		t.Fatalf("second full-bank submit = %+v deduped=%v, want the first", again, deduped)
	}
}
