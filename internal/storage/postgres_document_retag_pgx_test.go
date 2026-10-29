//go:build pgx

package storage

import (
	"context"
	"testing"
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
