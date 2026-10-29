//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
)

// A retag replaces only the document-derived tags on the PostgreSQL backend: the
// label-derived tag a unit carries is re-appended, and a retag with no label keys
// still replaces every tag.
func TestPostgresRetagKeepsLabelDerivedTags(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	if _, err := store.PutDocument(ctx, "read-bank", "doc-1", Document{OriginalText: "text"}); err != nil {
		t.Fatal(err)
	}
	unitID := pgUUID("unit-1")
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: unitID, BankID: "read-bank", Text: "a fact", DocumentID: "doc-1", FactType: TypeWorld,
		Tags: []string{"work", "topic:algorithms"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.RetagDocumentMemories(ctx, "read-bank", "doc-1", DocumentScopeChange{
		OldTags: []string{"work"}, NewTags: []string{"team-a"}, LabelKeys: map[string]bool{"topic": true},
	}); err != nil {
		t.Fatal(err)
	}
	unit, err := store.GetMemory(ctx, "read-bank", unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unit.Tags) != 2 || unit.Tags[0] != "team-a" || unit.Tags[1] != "topic:algorithms" {
		t.Fatalf("tags after retag = %v, want [team-a topic:algorithms]", unit.Tags)
	}

	if _, err := store.RetagDocumentMemories(ctx, "read-bank", "doc-1", DocumentScopeChange{
		OldTags: []string{"team-a"}, NewTags: []string{"work"},
	}); err != nil {
		t.Fatal(err)
	}
	unit, err = store.GetMemory(ctx, "read-bank", unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unit.Tags) != 1 || unit.Tags[0] != "work" {
		t.Fatalf("tags without label keys = %v, want [work]", unit.Tags)
	}
}

// A retag that moves a unit requeues it and the surviving co-sources of the
// observations the sweep deleted — both by clearing the consolidation marker,
// which is what the worker picks `consolidated_at IS NULL` up — and leaves an
// unrelated consolidated memory alone (documents.py:578-588, :597-607).
func TestPostgresRetagRequeuesMovedUnitsAndCoSources(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()
	if _, err := store.PutDocument(ctx, "read-bank", "doc-1", Document{
		OriginalText: "text", Tags: []string{"session:a"},
	}); err != nil {
		t.Fatal(err)
	}
	consolidated := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	factID := pgUUID("retag-fact")
	coSourceID := pgUUID("retag-co-source")
	bystanderID := pgUUID("retag-bystander")
	observationID := pgUUID("retag-observation")
	for _, unit := range []MemoryUnit{
		// The document's own fact, whose resolved scopes the retag moves.
		{ID: factID, BankID: "read-bank", Text: "a fact", DocumentID: "doc-1",
			FactType: TypeWorld, Tags: []string{"session:a"}, ConsolidatedAt: &consolidated},
		// A co-source of the observation that is no part of the document.
		{ID: coSourceID, BankID: "read-bank", Text: "another fact",
			FactType: TypeExperience, Tags: []string{"other"}, ConsolidatedAt: &consolidated},
		// A consolidated memory the update touches nowhere.
		{ID: bystanderID, BankID: "read-bank", Text: "unrelated",
			FactType: TypeExperience, Tags: []string{"other"}, ConsolidatedAt: &consolidated},
		{ID: observationID, BankID: "read-bank", Text: "an observation",
			FactType: TypeObservation, SourceMemoryIDs: []string{factID, coSourceID},
			ConsolidatedAt: &consolidated},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}

	outcome, err := store.RetagDocumentMemories(ctx, "read-bank", "doc-1", DocumentScopeChange{
		OldTags: []string{"session:a"}, NewTags: []string{"team-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.InvalidatedObservations != 1 || outcome.RequeuedUnits != 1 {
		t.Fatalf("outcome = %+v, want 1 observation invalidated and 1 unit requeued", outcome)
	}
	if _, err := store.GetMemory(ctx, "read-bank", observationID); err != ErrMemoryNotFound {
		t.Fatalf("observation read = %v, want not found", err)
	}
	for _, id := range []string{factID, coSourceID} {
		unit, err := store.GetMemory(ctx, "read-bank", id)
		if err != nil {
			t.Fatal(err)
		}
		if unit.ConsolidatedAt != nil {
			t.Fatalf("%s still consolidated at %v", id, unit.ConsolidatedAt)
		}
	}
	bystander, err := store.GetMemory(ctx, "read-bank", bystanderID)
	if err != nil {
		t.Fatal(err)
	}
	if bystander.ConsolidatedAt == nil {
		t.Fatal("the update cleared an unrelated memory's consolidation marker")
	}
}
