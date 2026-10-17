//go:build pgx

package storage

import (
	"context"
	"testing"
)

// The reference pins this shape (its entity-links system test): once the
// document that named an entity is gone, the entity's mention_count must equal
// the number of facts still naming it, not the number that ever did. wenyuan's
// counter is otherwise increment-only, so it drifts upward on every replace or
// delete unless the postings' mentions are given back first.
func TestPostgresDocumentDeleteGivesBackEntityMentions(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	for _, doc := range []string{"doc-1", "doc-2"} {
		if _, err := store.PutDocument(ctx, "read-bank", doc, Document{OriginalText: "Alice ships the release"}); err != nil {
			t.Fatal(err)
		}
	}
	alice, err := store.ResolveEntity(ctx, "read-bank", "Alice", EntityKindRegular, true)
	if err != nil {
		t.Fatal(err)
	}
	// Two facts in doc-1, one in doc-2: deleting doc-1 must take the count from
	// three to one and leave the entity, still named by doc-2's fact.
	units := []struct{ id, doc string }{
		{pgUUID("unit-1"), "doc-1"},
		{pgUUID("unit-2"), "doc-1"},
		{pgUUID("unit-3"), "doc-2"},
	}
	for _, u := range units {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: u.id, BankID: "read-bank", Text: "Alice ships the release", DocumentID: u.doc, FactType: TypeWorld,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertUnitEntity(ctx, u.id, alice.ID); err != nil {
			t.Fatal(err)
		}
	}

	if got := storedMentionCount(t, store, alice.ID); got != 3 {
		t.Fatalf("mention_count with three facts = %d, want 3", got)
	}

	if _, err := store.DeleteDocument(ctx, "read-bank", "doc-1"); err != nil {
		t.Fatal(err)
	}
	if got := storedMentionCount(t, store, alice.ID); got != 1 {
		t.Fatalf("mention_count after the document went = %d, want 1", got)
	}
}

// storedMentionCount reads the entity's stored count through the unfiltered
// listing, the path that serves the entities row's column.
func storedMentionCount(t *testing.T, store Store, entityID string) int {
	t.Helper()
	page, err := store.ListEntities(context.Background(), EntityQuery{BankID: "read-bank", ID: entityID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entities) != 1 {
		t.Fatalf("entity listing = %d rows, want 1", len(page.Entities))
	}
	return page.Entities[0].MentionCount
}
