package storage

import (
	"context"
	"testing"
)

// test-mental-model-freshness: the reflection lookup carries each perspective's
// freshness, so the loop can tell a current page from a stale one.
func TestReflectMentalModelsCarriesFreshness(t *testing.T) {
	store := reflectTestStore(t)
	store.SeedMentalModel("b1", ReflectMentalModel{ID: "mm-fresh", Text: "alpha coverage"})
	store.SeedMentalModel("b1", ReflectMentalModel{ID: "mm-stale", Text: "alpha drift", Stale: true})

	got, err := store.ReflectMentalModels(context.Background(), "b1", "alpha", 0)
	if err != nil {
		t.Fatalf("mental-model retrieval failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("retrieved %d perspectives, want 2: %+v", len(got), got)
	}
	byID := map[string]ReflectCitation{}
	for _, citation := range got {
		byID[citation.ID] = citation
	}
	if !byID["mm-fresh"].Fresh {
		t.Fatalf("a current perspective must be marked fresh: %+v", byID["mm-fresh"])
	}
	if byID["mm-stale"].Fresh {
		t.Fatalf("a stale perspective must not be marked fresh: %+v", byID["mm-stale"])
	}
}
