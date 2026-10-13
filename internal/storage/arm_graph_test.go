package storage

import (
	"context"
	"strconv"
	"testing"
)

// The graph arm reaches memories connected to what the lexical arms matched,
// which is what makes it distinct from the lexical arms: a linked fact need not
// share the query's wording.
func TestGraphArmReachesLinkedMemories(t *testing.T) {
	store := NewMemoryStore("public")
	seedBank(t, store, "b1",
		RecallMatch{ID: "seed", Text: "alice works at google", Type: "world"},
		// Deliberately shares no word with the query.
		RecallMatch{ID: "linked", Text: "the campus is in mountain view", Type: "world"},
		RecallMatch{ID: "unlinked", Text: "unrelated note about weather", Type: "world"},
	)
	if err := store.UpsertLink(context.TODO(), MemoryLink{
		ID: "l1", BankID: "b1", FromID: "seed", ToID: "linked", LinkType: LinkSemantic,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := store.Recall(context.TODO(), RecallQuery{BankID: "b1", Query: "alice google"})
	if err != nil {
		t.Fatal(err)
	}
	ids := recallIDs(result)
	if !containsID(ids, "seed") {
		t.Fatalf("the lexical match is missing: %v", ids)
	}
	if !containsID(ids, "linked") {
		t.Fatalf("the linked memory was not reached: %v", ids)
	}
	if containsID(ids, "unlinked") {
		t.Fatalf("an unlinked memory must not be reached: %v", ids)
	}
}

// The graph arm is reported in the trace, so a caller can see it contributed.
func TestGraphArmAppearsInTrace(t *testing.T) {
	store := NewMemoryStore("public")
	seedBank(t, store, "b1",
		RecallMatch{ID: "seed", Text: "alice works at google", Type: "world"},
		RecallMatch{ID: "linked", Text: "the campus is in mountain view", Type: "world"},
	)
	if err := store.UpsertLink(context.TODO(), MemoryLink{
		ID: "l1", BankID: "b1", FromID: "seed", ToID: "linked", LinkType: LinkSemantic,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Recall(context.TODO(), RecallQuery{BankID: "b1", Query: "alice google"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Trace.StageCounts[ArmGraph] == 0 {
		t.Fatalf("the graph arm is absent from the trace: %+v", result.Trace.StageCounts)
	}
}

// Disabling the graph arm removes it, so it can be turned off like any other.
func TestGraphArmCanBeDisabled(t *testing.T) {
	store := NewMemoryStore("public")
	seedBank(t, store, "b1",
		RecallMatch{ID: "seed", Text: "alice works at google", Type: "world"},
		RecallMatch{ID: "linked", Text: "the campus is in mountain view", Type: "world"},
	)
	if err := store.UpsertLink(context.TODO(), MemoryLink{
		ID: "l1", BankID: "b1", FromID: "seed", ToID: "linked", LinkType: LinkSemantic,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Recall(context.TODO(), RecallQuery{
		BankID: "b1", Query: "alice google", DisabledArms: []string{ArmGraph},
	})
	if err != nil {
		t.Fatal(err)
	}
	if containsID(recallIDs(result), "linked") {
		t.Fatalf("the graph arm returned a result while disabled: %v", recallIDs(result))
	}
}

// The walk is bounded, so a densely linked bank cannot turn one query into a
// traversal of everything.
func TestGraphArmIsBounded(t *testing.T) {
	store := NewMemoryStore("public")
	matches := []RecallMatch{{ID: "seed", Text: "alice works at google", Type: "world"}}
	for i := 0; i < graphNodeBudget*3; i++ {
		matches = append(matches, RecallMatch{ID: idFor(i), Text: "filler", Type: "world"})
	}
	seedBank(t, store, "b1", matches...)
	// Chain every filler memory onto the seed, far past the budget.
	for i := 0; i < graphNodeBudget*3; i++ {
		from := "seed"
		if i > 0 {
			from = idFor(i - 1)
		}
		if err := store.UpsertLink(context.TODO(), MemoryLink{
			ID: "l" + idFor(i), BankID: "b1", FromID: from, ToID: idFor(i), LinkType: LinkSemantic,
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.Recall(context.TODO(), RecallQuery{BankID: "b1", Query: "alice google"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) > graphNodeBudget+1 {
		t.Fatalf("the walk returned %d matches, over the %d node budget", len(result.Matches), graphNodeBudget)
	}
}

// A bank with no links yields nothing from the graph arm, so a query is not
// answered by a walk that found no edges.
func TestGraphArmEmptyWithoutLinks(t *testing.T) {
	store := NewMemoryStore("public")
	seedBank(t, store, "b1", RecallMatch{ID: "m1", Text: "alice works at google", Type: "world"})
	hits := store.runGraphArm(RecallQuery{BankID: "b1", Query: "alice google"}, []string{"world"}, "alice google")
	if len(hits) != 0 {
		t.Fatalf("the graph arm returned %d hits without any links", len(hits))
	}
}

// A memory reached by the walk is not also reported as a seed, so the fusion
// does not count it twice.
func TestGraphArmExcludesSeeds(t *testing.T) {
	store := NewMemoryStore("public")
	seedBank(t, store, "b1", RecallMatch{ID: "seed", Text: "alice works at google", Type: "world"})
	if err := store.UpsertLink(context.TODO(), MemoryLink{
		ID: "l1", BankID: "b1", FromID: "seed", ToID: "seed", LinkType: LinkSemantic,
	}); err != nil {
		t.Fatal(err)
	}
	hits := store.runGraphArm(RecallQuery{BankID: "b1", Query: "alice google"}, []string{"world"}, "alice google")
	for _, hit := range hits {
		if hit.match.ID == "seed" {
			t.Fatal("a seed must not be re-emitted by the graph arm")
		}
	}
}

// idFor builds a stable filler identifier.
func idFor(i int) string {
	return "m" + strconv.Itoa(i)
}
