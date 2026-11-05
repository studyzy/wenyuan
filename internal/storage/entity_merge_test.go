package storage

import (
	"context"
	"testing"
)

// The trigram gate: a candidate whose name shares too few trigrams with the
// mention is not merged even though the sequence score would accept it.
func TestEntityMergeFloorRejectsLowTrigramCandidate(t *testing.T) {
	// "Mike" and "Miek" are one transposition apart, so their sequence ratio
	// clears the bar, but their trigram sets share too little to clear the 0.3
	// floor.
	if got := entitySimilarity("Mike", "Miek"); got < EntitySimilarityThreshold {
		t.Fatalf("fixture: score %v should clear the similarity threshold", got)
	}
	if entityMergeFloorPasses("Mike", "Miek", DefaultEntityMergeMinSimilarity) {
		t.Fatalf("fixture: trigram score %v should be below the merge floor", trigramJaccard("mike", "miek"))
	}

	store := NewMemoryStore("public")
	store.SetTuning(Tuning{EntityMergeMinSimilarity: DefaultEntityMergeMinSimilarity})
	store.entities["e1"] = Entity{ID: "e1", BankID: "b1", CanonicalName: "Miek", EntityKind: EntityKindRegular}

	resolved := store.resolveEntityLocked("b1", RetainEntity{Text: "Mike"}, true, nil, nil)
	if resolved.ID == "e1" {
		t.Fatal("a candidate below the merge floor must not be merged onto")
	}
	if resolved.CanonicalName != "Mike" {
		t.Fatalf("resolved = %+v, want a new entity named Mike", resolved)
	}
}

// A candidate that clears the merge floor still needs the weighted score to
// reach the bar; name similarity alone contributes at most half of it.
func TestEntityMergeFloorAdmitsCloseCandidate(t *testing.T) {
	store := NewMemoryStore("public")
	store.SetTuning(Tuning{EntityMergeMinSimilarity: DefaultEntityMergeMinSimilarity})
	store.entities["e1"] = Entity{ID: "e1", BankID: "b1", CanonicalName: "Mikes", EntityKind: EntityKindRegular}

	resolved := store.resolveEntityLocked("b1", RetainEntity{Text: "Mike"}, true, nil, nil)
	if resolved.ID == "e1" {
		t.Fatal("name evidence alone must not reach the merge bar")
	}
	if resolved.CanonicalName != "Mike" {
		t.Fatalf("resolved = %+v, want a new entity named Mike", resolved)
	}
}

// The candidate-fetch floor and the merge gate are independent settings. A
// name whose trigram set is identical to the candidate's scores a full mark,
// so a zero merge floor admits it while a floor above one rejects it; the
// probe floor is a separate value the scoring pass never reads.
func TestEntityMergeFloorIsIndependentOfProbeFloor(t *testing.T) {
	candidates := []Entity{{ID: "e1", CanonicalName: "Mike!"}}
	if _, found, err := bestEntityCandidate(candidates, "Mike", 0, entityMentionContext{}); err != nil || !found {
		t.Fatalf("a zero merge floor admits a full-mark candidate: found=%v err=%v", found, err)
	}
	if _, found, err := bestEntityCandidate(candidates, "Mike", 1.0001, entityMentionContext{}); err != nil || found {
		t.Fatalf("a merge floor above one must reject even a full-mark candidate: found=%v err=%v", found, err)
	}
}

// bestEntityCandidate applies the merge floor before scoring. A name with an
// identical trigram set takes the full-mark path, while a name-only near miss
// stays below the weighted bar whichever floor admitted it.
func TestBestEntityCandidateHonoursMergeFloor(t *testing.T) {
	full := []Entity{{ID: "e1", CanonicalName: "Mike!"}}
	if _, found, err := bestEntityCandidate(full, "Mike", 0, entityMentionContext{}); err != nil || !found {
		t.Fatalf("floor 0 admits a full-mark candidate: found=%v err=%v", found, err)
	}
	if _, found, err := bestEntityCandidate(full, "Mike", DefaultEntityMergeMinSimilarity, entityMentionContext{}); err != nil || !found {
		t.Fatalf("the default floor admits an identical trigram set: found=%v err=%v", found, err)
	}
	if _, found, err := bestEntityCandidate(full, "Mike", 1.0001, entityMentionContext{}); err != nil || found {
		t.Fatalf("a merge floor above one was not applied: found=%v err=%v", found, err)
	}
	near := []Entity{{ID: "e2", CanonicalName: "Miek"}}
	if _, found, err := bestEntityCandidate(near, "Mike", 0, entityMentionContext{}); err != nil || found {
		t.Fatalf("name evidence alone must stay below the bar: found=%v err=%v", found, err)
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

// A cutoff above the pair's similarity leaves the two names unclustered by the
// in-batch pass. Resolution is a separate stage, so this reads the pass's own
// output rather than the entity count a whole write ends with.
func TestIntrabatchMergeHonoursTheConfiguredThreshold(t *testing.T) {
	entities := []RetainEntity{{Text: "Smith John"}, {Text: "John Smith"}}
	if canonical := intrabatchCanonicalNames(entities, true, DefaultEntityIntrabatchMergeSimilarity); canonical["smith john"] != canonical["john smith"] {
		t.Fatalf("the default cutoff should cluster the variants: %v", canonical)
	}
	strict := intrabatchCanonicalNames(entities, true, 1.0001)
	if strict["smith john"] == strict["john smith"] {
		t.Fatalf("a cutoff above the pair's similarity must leave the variants unclustered: %v", strict)
	}
}
