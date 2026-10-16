package storage

import (
	"context"
	"testing"
)

// The reference's second trigram gate: a candidate whose name shares too few
// trigrams with the mention is not merged even though the Jaro-Winkler score
// would accept it.
func TestEntityMergeFloorRejectsLowTrigramCandidate(t *testing.T) {
	// "Mike" and "Miek" are one transposition apart, so they score above the
	// Jaro-Winkler threshold, but their trigram sets share too little to clear
	// the 0.3 floor.
	if got := entitySimilarity("Mike", "Miek"); got < EntitySimilarityThreshold {
		t.Fatalf("fixture: score %v should clear the similarity threshold", got)
	}
	if entityMergeFloorPasses("Mike", "Miek", DefaultEntityMergeMinSimilarity) {
		t.Fatalf("fixture: trigram score %v should be below the merge floor", trigramJaccard("mike", "miek"))
	}

	store := NewMemoryStore("public")
	store.SetTuning(Tuning{EntityMergeMinSimilarity: DefaultEntityMergeMinSimilarity})
	store.entities["e1"] = Entity{ID: "e1", BankID: "b1", CanonicalName: "Miek", EntityKind: EntityKindRegular}

	resolved := store.resolveEntityLocked("b1", RetainEntity{Text: "Mike"}, true)
	if resolved.ID == "e1" {
		t.Fatal("a candidate below the merge floor must not be merged onto")
	}
	if resolved.CanonicalName != "Mike" {
		t.Fatalf("resolved = %+v, want a new entity named Mike", resolved)
	}
}

// A candidate that clears the merge floor still merges when the Jaro-Winkler
// score accepts it.
func TestEntityMergeFloorAdmitsCloseCandidate(t *testing.T) {
	store := NewMemoryStore("public")
	store.SetTuning(Tuning{EntityMergeMinSimilarity: DefaultEntityMergeMinSimilarity})
	store.entities["e1"] = Entity{ID: "e1", BankID: "b1", CanonicalName: "Mikes", EntityKind: EntityKindRegular}

	resolved := store.resolveEntityLocked("b1", RetainEntity{Text: "Mike"}, true)
	if resolved.ID != "e1" {
		t.Fatalf("resolved = %+v, want the existing Mikes entity", resolved)
	}
}

// The candidate-fetch floor and the merge floor are independent: the merge gate
// is driven by EntityMergeMinSimilarity, not by the probe floor, so raising the
// probe floor alone changes nothing and raising the merge floor alone rejects a
// candidate the score would otherwise accept.
func TestEntityMergeFloorIsIndependentOfProbeFloor(t *testing.T) {
	// A high probe floor alone leaves the merge untouched: the store does not
	// consult it when scoring.
	store := NewMemoryStore("public")
	store.SetTuning(Tuning{EntityCandidateSimilarity: 0.99, EntityMergeMinSimilarity: DefaultEntityMergeMinSimilarity})
	store.entities["e1"] = Entity{ID: "e1", BankID: "b1", CanonicalName: "Mikes", EntityKind: EntityKindRegular}
	if resolved := store.resolveEntityLocked("b1", RetainEntity{Text: "Mike"}, true); resolved.ID != "e1" {
		t.Fatalf("the probe floor must not gate the merge: %+v", resolved)
	}

	// A high merge floor alone rejects the same candidate.
	strict := NewMemoryStore("public")
	strict.SetTuning(Tuning{EntityMergeMinSimilarity: 0.99})
	strict.entities["e1"] = Entity{ID: "e1", BankID: "b1", CanonicalName: "Mikes", EntityKind: EntityKindRegular}
	if resolved := strict.resolveEntityLocked("b1", RetainEntity{Text: "Mike"}, true); resolved.ID == "e1" {
		t.Fatalf("the merge floor must gate the merge: %+v", resolved)
	}
}

// bestEntityCandidate applies the merge floor before scoring, so the same
// candidate set merges or not purely by the floor it is given. The probe floor
// is a different value, applied by the candidate fetch rather than here.
func TestBestEntityCandidateHonoursMergeFloor(t *testing.T) {
	candidates := []Entity{{ID: "e1", CanonicalName: "Miek"}}
	if _, found, err := bestEntityCandidate(candidates, "Mike", 0); err != nil || !found {
		t.Fatalf("floor 0 admits the candidate the score accepts: found=%v err=%v", found, err)
	}
	if _, found, err := bestEntityCandidate(candidates, "Mike", DefaultEntityMergeMinSimilarity); err != nil || found {
		t.Fatalf("the merge floor was not applied: found=%v err=%v", found, err)
	}
}

// countBankEntities returns how many entities a bank holds.
func countBankEntities(store *MemoryStore, bankID string) int {
	count := 0
	for _, entity := range store.entities {
		if entity.BankID == bankID {
			count++
		}
	}
	return count
}

// Two spellings of one name that arrive in the same document are merged before
// either is scored, so the write creates one entity rather than two.
func TestIntrabatchMergeJoinsSameDocumentVariants(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Items: []RetainItem{{
			Content:         "John Smith and Smith John are the same person.",
			DocumentID:      "doc-1",
			ResolveEntities: true,
			Entities:        []RetainEntity{{Text: "Smith John"}, {Text: "John Smith"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := countBankEntities(store, "b1"); got != 1 {
		t.Fatalf("created %d entities, want the two variants merged into one", got)
	}
}

// The merge is off when resolution is off: names the caller wrote literally
// stay two.
func TestIntrabatchMergeSkipsLiterallyWrittenNames(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Items: []RetainItem{{
			Content:    "John Smith and Smith John are the same person.",
			DocumentID: "doc-1",
			Entities:   []RetainEntity{{Text: "Smith John"}, {Text: "John Smith"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := countBankEntities(store, "b1"); got != 2 {
		t.Fatalf("created %d entities, want the two literal names kept apart", got)
	}
}

// A threshold above the pair's similarity leaves the variants separate, which
// is what makes the configured cutoff observable.
func TestIntrabatchMergeHonoursTheConfiguredThreshold(t *testing.T) {
	store := NewMemoryStore("public")
	store.SetTuning(Tuning{EntityIntrabatchMerge: 1.0001})
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Items: []RetainItem{{
			Content:         "John Smith and Smith John are the same person.",
			DocumentID:      "doc-1",
			ResolveEntities: true,
			Entities:        []RetainEntity{{Text: "Smith John"}, {Text: "John Smith"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := countBankEntities(store, "b1"); got != 2 {
		t.Fatalf("created %d entities, want the variants kept apart above the threshold", got)
	}
}
