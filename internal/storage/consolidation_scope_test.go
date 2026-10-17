package storage

import (
	"context"
	"encoding/json"
	"testing"
)

// Scoped observation reads and writes keep two scopes apart, and the produced
// observation carries its scope both as tags and in the metadata the
// observation-scope listing reads.
func TestConsolidationScopeIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")

	scopedStore, ok := any(store).(ConsolidationScopeStore)
	if !ok {
		t.Fatal("the memory store must provide the consolidation scope capability")
	}
	if _, err := scopedStore.CreateObservationInScope(ctx, "b1", "conclusion a", nil, []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := scopedStore.CreateObservationInScope(ctx, "b1", "conclusion b", nil, []string{"b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := scopedStore.CreateObservationInScope(ctx, "b1", "untagged conclusion", nil, nil); err != nil {
		t.Fatal(err)
	}

	a, err := scopedStore.ConsolidationObservationsInScope(ctx, "b1", []string{"a"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].Text != "conclusion a" {
		t.Fatalf("scope a observations = %+v", a)
	}
	if len(a[0].Scope) != 1 || a[0].Scope[0] != "a" {
		t.Fatalf("observation scope = %v, want [a]", a[0].Scope)
	}

	untagged, err := scopedStore.ConsolidationObservationsInScope(ctx, "b1", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(untagged) != 1 || untagged[0].Text != "untagged conclusion" {
		t.Fatalf("untagged observations = %+v", untagged)
	}

	// The scope order of the request must not matter.
	swapped, err := scopedStore.ConsolidationObservationsInScope(ctx, "b1", []string{"a"}, 10)
	if err != nil || len(swapped) != 1 {
		t.Fatalf("swapped scope = %+v (err %v)", swapped, err)
	}

	// The observation-scope listing reports the observation's tag set.
	page, err := store.ListObservationScopes(ctx, "b1", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, scope := range page.Scopes {
		if len(scope.Tags) == 1 && scope.Tags[0] == "a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("scopes = %+v, want [a]", page.Scopes)
	}
}

// A fact read for consolidation carries its own tags and its raw scope request,
// so the engine can resolve the scopes it folds at instead of pooling every
// unconsolidated fact together.
func TestConsolidationFactsCarryScope(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	unit := MemoryUnit{
		ID: "f1", BankID: "b1", Text: "a fact", FactType: TypeWorld,
		Tags:              []string{"z", "a"},
		ObservationScopes: json.RawMessage(`"per_tag"`),
	}
	if _, err := store.UpsertUnit(ctx, unit); err != nil {
		t.Fatal(err)
	}
	facts, err := store.ConsolidationFacts(ctx, "b1", 10)
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts = %+v (err %v)", facts, err)
	}
	if len(facts[0].Tags) != 2 || facts[0].Tags[0] != "a" || facts[0].Tags[1] != "z" {
		t.Fatalf("fact tags = %v, want sorted [a z]", facts[0].Tags)
	}
	if string(facts[0].ObservationScopes) != `"per_tag"` {
		t.Fatalf("observation scopes = %s", facts[0].ObservationScopes)
	}
}
