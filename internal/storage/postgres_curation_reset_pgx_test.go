//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
)

// Clearing a memory's observations requeues it and submits the rebuilding pass
// even when the sweep removed nothing: the clear is an explicit reset request
// (engine/memory_engine.py:12540-12565 at v1.1.0, #5731).
func TestPostgresClearObservationsRequeuesWithNothingToRemove(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "clear_obs_reset")
	// The default is off, so the bank turns automatic consolidation on — the
	// gate the reference reads at :12553.
	if _, err := pg.CreateBank(ctx, Bank{
		BankID: "b1", Name: "b1", Config: map[string]any{"enable_auto_consolidation": true},
	}); err != nil {
		t.Fatal(err)
	}
	const source = "bbbbbbbb-0000-0000-0000-000000000001"
	consolidated := time.Now().UTC().Add(-time.Hour)
	if _, err := pg.UpsertUnit(ctx, MemoryUnit{
		ID: source, BankID: "b1", Text: "the deployment runs on Tuesdays",
		FactType: TypeWorld, ConsolidatedAt: &consolidated,
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := pg.DeleteObservations(ctx, "b1", source)
	if err != nil {
		t.Fatalf("clear observations: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 — the fixture carries no observation", removed)
	}
	unit, err := pg.GetMemory(ctx, "b1", source)
	if err != nil {
		t.Fatal(err)
	}
	if unit.ConsolidatedAt != nil {
		t.Fatalf("consolidated_at = %v, want nil: the clear is a reset request", unit.ConsolidatedAt)
	}
	// The requeued memory is what the pass rebuilds from, so the pass is queued
	// whether or not anything went.
	ops, err := pg.ListOperations(ctx, OperationQuery{BankID: "b1", Type: OperationTypeConsolidate})
	if err != nil {
		t.Fatal(err)
	}
	if len(ops.Operations) != 1 {
		t.Fatalf("queued %d consolidation operations, want 1", len(ops.Operations))
	}
}

// A refused retain's recall drops only the rows it names, leaving an attachment
// the document already carried (the reference scopes it the same way,
// engine/memory_engine.py:8440-8455).
func TestPostgresRemoveAttachmentRowsNarrows(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "remove_rows")
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []Attachment{
		{BankID: "b1", DocumentID: "doc-1", AttachmentHash: "hash-kept", ShortID: "kept", MediaType: "image/png", StorageKey: "tenants/x/key-kept"},
		{BankID: "b1", DocumentID: "doc-1", AttachmentHash: "hash-new", ShortID: "dropped", MediaType: "image/png", StorageKey: "tenants/x/key-new"},
		{BankID: "b1", DocumentID: "doc-2", AttachmentHash: "hash-other", ShortID: "dropped", MediaType: "image/png", StorageKey: "tenants/x/key-other"},
	} {
		if err := pg.PutAttachment(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.RemoveAttachmentRows(ctx, "b1", "doc-1", []string{"dropped"}); err != nil {
		t.Fatalf("remove rows: %v", err)
	}
	kept, err := pg.ListAttachments(ctx, "b1", "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].ShortID != "kept" {
		t.Fatalf("doc-1 attachments = %+v, want only the one the document already had", kept)
	}
	// Scoped to the document: another document's row with the same short id stays.
	other, err := pg.ListAttachments(ctx, "b1", "doc-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 {
		t.Fatalf("doc-2 attachments = %+v, want its own row untouched", other)
	}
}
