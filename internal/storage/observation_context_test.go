package storage

import (
	"context"
	"testing"
)

// An observation carries no context of its own: the reference's insert never
// names the context column (engine/memories/pg/consolidation.py:268-320), so a
// recall result reports `context: null` for it while a fact with no context
// reports "". The write path has to keep the distinction, otherwise the store
// folds the two together before a reader can tell them apart.
func TestObservationHasNoContext(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	const source = "88888888-8888-8888-8888-888888888888"
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: source, BankID: "b1", Text: "the deployment runs on Tuesdays", FactType: TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateObservation(ctx, "b1", "deployments happen weekly", []string{source}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateObservationInScope(ctx, "b1", "scoped conclusion", []string{source}, []string{"user:bob"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListMemories(ctx, MemoryQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, unit := range page.Memories {
		if unit.FactType != TypeObservation {
			if unit.ContextAbsent {
				t.Fatalf("fact %s reports an absent context", unit.ID)
			}
			continue
		}
		seen++
		if !unit.ContextAbsent || unit.Context != "" {
			t.Fatalf("observation %s context = %q (absent %v), want absent and empty",
				unit.ID, unit.Context, unit.ContextAbsent)
		}
	}
	if seen < 2 {
		t.Fatalf("listed %d observations, want both created ones", seen)
	}
}
