package storage

import (
	"context"
	"testing"
)

// seedCooccurrenceUnit installs one live unit in a bank so an edit and a prune
// can be exercised without retain's extractor.
func seedCooccurrenceUnit(t *testing.T, store *MemoryStore, bankID, unitID string) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.units[unitID] = MemoryUnit{ID: unitID, BankID: bankID, DocumentID: "doc-1", FactType: TypeWorld, Metadata: map[string]any{}}
}

// An entity-set edit records the pairs the unit's resulting set witnesses,
// which is what the reference does when it re-links an edited unit. Without it
// the entity graph of a bank whose entities were curated never gains the pairs
// those edits created.
func TestMemoryEntityEditRecordsCooccurrences(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	seedCooccurrenceUnit(t, store, "b1", "u1")

	if err := store.SetUnitEntities(ctx, "b1", "u1", []string{"Alice", "Berlin"}, true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	graph, err := store.EntityGraph(ctx, EntityGraphQuery{BankID: "b1", Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 1 || graph.Edges[0].Weight != 1 {
		t.Fatalf("after the edit the graph = %d edge(s) %+v, want the one pair the set witnesses", len(graph.Edges), graph.Edges)
	}

	// A second edit that keeps both and adds a third records every pair of the
	// resulting set, which is the reference's re-link semantics: the surviving
	// pair is counted again rather than only the additions.
	if err := store.SetUnitEntities(ctx, "b1", "u1", []string{"Alice", "Berlin", "Carol"}, true); err != nil {
		t.Fatalf("second edit: %v", err)
	}
	graph, err = store.EntityGraph(ctx, EntityGraphQuery{BankID: "b1", Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 3 {
		t.Fatalf("after the second edit the graph = %d edge(s), want the three pairs of the set", len(graph.Edges))
	}
	alice := store.entityIDForName(t, "b1", "Alice")
	berlin := store.entityIDForName(t, "b1", "Berlin")
	counted := 0
	for _, edge := range graph.Edges {
		if (edge.Source == alice && edge.Target == berlin) || (edge.Source == berlin && edge.Target == alice) {
			counted = edge.Weight
		}
	}
	if counted != 2 {
		t.Fatalf("the surviving pair's weight = %d, want 2: the reference re-links the whole resolved set", counted)
	}
}

// An edit that drops one entity of a pair leaves the recorded co-occurrence
// behind: both entities still exist, but no unit witnesses them together any
// more. The entity prune is what removes it, which is the case its foreign-key
// cascade cannot see — neither endpoint is orphaned.
func TestMemoryEntityPruneDropsUnwitnessedPairs(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	// The pair is witnessed once, and a second unit keeps both entities alive
	// after it stops being witnessed.
	seedCooccurrenceUnit(t, store, "b1", "u1")
	seedCooccurrenceUnit(t, store, "b1", "u2")
	if err := store.SetUnitEntities(ctx, "b1", "u1", []string{"Alice", "Berlin"}, true); err != nil {
		t.Fatalf("edit u1: %v", err)
	}
	if err := store.SetUnitEntities(ctx, "b1", "u2", []string{"Alice", "Berlin"}, true); err != nil {
		t.Fatalf("edit u2: %v", err)
	}
	if err := store.SetUnitEntities(ctx, "b1", "u2", []string{"Alice"}, true); err != nil {
		t.Fatalf("edit u2 again: %v", err)
	}
	// Both entities still have a posting, so neither is an orphan.
	alice := store.entityIDForName(t, "b1", "Alice")
	if err := store.SetUnitEntities(ctx, "b1", "u1", []string{"Berlin"}, true); err != nil {
		t.Fatalf("edit u1 again: %v", err)
	}

	if err := store.PruneEntity(ctx, "b1", alice); err != nil {
		t.Fatalf("prune: %v", err)
	}

	graph, err := store.EntityGraph(ctx, EntityGraphQuery{BankID: "b1", Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 0 {
		t.Fatalf("graph keeps %d edge(s) no unit witnesses: %+v", len(graph.Edges), graph.Edges)
	}
}

// A pair another unit still witnesses survives the prune of an entity it is
// incident to: the row is stale only when nothing in the bank sees the two
// together any more.
func TestMemoryEntityPruneKeepsWitnessedPairs(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	seedCooccurrenceUnit(t, store, "b1", "u1")
	seedCooccurrenceUnit(t, store, "b1", "u2")
	for _, unitID := range []string{"u1", "u2"} {
		if err := store.SetUnitEntities(ctx, "b1", unitID, []string{"Alice", "Berlin"}, true); err != nil {
			t.Fatalf("edit %s: %v", unitID, err)
		}
	}
	if err := store.PruneEntity(ctx, "b1", store.entityIDForName(t, "b1", "Alice")); err != nil {
		t.Fatalf("prune: %v", err)
	}

	graph, err := store.EntityGraph(ctx, EntityGraphQuery{BankID: "b1", Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("graph = %d edge(s), want the pair two units still witness", len(graph.Edges))
	}
}

// entityIDForName returns the identity a name resolves to in the bank.
func (s *MemoryStore) entityIDForName(t *testing.T, bankID, name string) string {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entity := range s.entities {
		if entity.BankID == bankID && entity.CanonicalName == name {
			return entity.ID
		}
	}
	t.Fatalf("no entity named %q in %s", name, bankID)
	return ""
}
