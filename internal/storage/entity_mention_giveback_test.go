package storage

import (
	"context"
	"testing"
)

// seedMentionedUnit installs a document's unit with one posting to a fresh
// entity whose stored mention_count is set to count, so a test can watch the
// give-back without going through retain's extractor.
func seedMentionedUnit(t *testing.T, store *MemoryStore, bankID, docID string, count int) (string, string) {
	t.Helper()
	unitID := newID()
	store.units[unitID] = MemoryUnit{ID: unitID, BankID: bankID, DocumentID: docID, FactType: TypeWorld}
	entityID := newID()
	store.entities[entityID] = Entity{
		ID:            entityID,
		BankID:        bankID,
		CanonicalName: "Alice",
		EntityKind:    EntityKindRegular,
		MentionCount:  count,
	}
	store.unitEntities[unitID] = []string{entityID}
	return unitID, entityID
}

// Deleting a document gives back the mention count its units' postings
// contributed: the counter is otherwise increment-only, so without the
// give-back it drifts upward on every replace or delete.
func TestMemoryDeleteDocumentGivesBackEntityMentions(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	_, entityID := seedMentionedUnit(t, store, "b1", "doc-1", 2)
	// A second posting in a surviving document keeps the entity alive, so the
	// assertion reads the count rather than a prune that removed the row.
	addSurvivingUnit(store, "b1", "doc-2", entityID)
	store.documents["doc-1"] = Document{ID: "doc-1", BankID: "b1"}

	if _, err := store.DeleteDocument(ctx, "b1", "doc-1"); err != nil {
		t.Fatal(err)
	}
	if got := store.entities[entityID].MentionCount; got != 1 {
		t.Fatalf("mention_count after delete = %d, want 1 (two mentions less this posting)", got)
	}
}

// addSurvivingUnit installs a second unit naming entityID in a document that
// the test does not delete, so a give-back test is not defeated by the prune
// that removes an entity left with no postings at all.
func addSurvivingUnit(store *MemoryStore, bankID, docID, entityID string) {
	id := newID()
	store.units[id] = MemoryUnit{ID: id, BankID: bankID, DocumentID: docID, FactType: TypeWorld}
	store.unitEntities[id] = []string{entityID}
}

// The give-back is floored at zero: a posting whose entity already reads zero
// must not drive the count negative.
func TestMemoryDeleteDocumentFloorsMentionCountAtZero(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	_, entityID := seedMentionedUnit(t, store, "b1", "doc-1", 0)
	addSurvivingUnit(store, "b1", "doc-2", entityID)
	store.documents["doc-1"] = Document{ID: "doc-1", BankID: "b1"}

	if _, err := store.DeleteDocument(ctx, "b1", "doc-1"); err != nil {
		t.Fatal(err)
	}
	if got := store.entities[entityID].MentionCount; got != 0 {
		t.Fatalf("mention_count after delete = %d, want 0", got)
	}
}

// A replace clears the document's units before rebuilding them, so the old
// postings' mentions have to come back the same way a delete's do.
func TestMemoryReplaceDocumentGivesBackEntityMentions(t *testing.T) {
	store := NewMemoryStore("public")
	_, entityID := seedMentionedUnit(t, store, "b1", "doc-1", 3)

	store.clearDocumentUnitsLocked("b1", "doc-1")

	if got := store.entities[entityID].MentionCount; got != 2 {
		t.Fatalf("mention_count after replace = %d, want 2", got)
	}
}

// Invalidating a memory unlinks it, so its postings give their mentions back
// and are removed, matching the postgres archive move.
func TestMemoryInvalidationGivesBackEntityMentions(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	unitID, entityID := seedMentionedUnit(t, store, "b1", "doc-1", 1)

	state := "invalidated"
	if _, err := store.UpdateMemory(ctx, "b1", unitID, MemoryPatch{State: &state}); err != nil {
		t.Fatal(err)
	}
	if got := store.entities[entityID].MentionCount; got != 0 {
		t.Fatalf("mention_count after invalidation = %d, want 0", got)
	}
	if _, ok := store.unitEntities[unitID]; ok {
		t.Fatal("postings survived invalidation")
	}
}

// An edit that rewrites a unit's entity set both links the new names and
// unlinks the dropped ones, so the stored count tracks the set instead of
// drifting.
func TestMemorySetUnitEntitiesTracksMentionCount(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	unitID := newID()
	store.units[unitID] = MemoryUnit{ID: unitID, BankID: "b1", FactType: TypeWorld}

	if err := store.SetUnitEntities(ctx, "b1", unitID, []string{"Alice"}, false); err != nil {
		t.Fatal(err)
	}
	var entityID string
	for id, entity := range store.entities {
		if entity.CanonicalName == "Alice" {
			entityID = id
		}
	}
	if entityID == "" {
		t.Fatal("SetUnitEntities did not create the entity")
	}
	if got := store.entities[entityID].MentionCount; got != 1 {
		t.Fatalf("mention_count after link = %d, want 1", got)
	}

	if err := store.SetUnitEntities(ctx, "b1", unitID, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := store.entities[entityID].MentionCount; got != 0 {
		t.Fatalf("mention_count after unlink = %d, want 0", got)
	}
}
