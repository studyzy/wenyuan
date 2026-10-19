//go:build pgx

package storage

import (
	"context"
	"testing"
)

// seedPGCooccurrenceUnit creates a bank and one live unit in it, so an edit and
// a prune can be exercised through the store's own API on a real database.
func seedPGCooccurrenceUnit(t *testing.T, store Store, bankID, unitID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.GetBank(ctx, bankID); err != nil {
		if _, err := store.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
			t.Fatalf("create bank: %v", err)
		}
	}
	if _, err := store.PutDocument(ctx, bankID, "doc-1", Document{ID: "doc-1", BankID: bankID}); err != nil {
		// The second seeded unit shares the document with the first.
		if _, again := store.GetDocument(ctx, bankID, "doc-1"); again != nil {
			t.Fatalf("seed document: %v", err)
		}
	}
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: unitID, BankID: bankID, DocumentID: "doc-1", FactType: TypeWorld, Metadata: map[string]any{},
	}); err != nil {
		t.Fatalf("seed unit %s: %v", unitID, err)
	}
}

// pgEntityIDForName resolves a canonical name to its stored identity.
func pgEntityIDForName(t *testing.T, store Store, bankID, name string) string {
	t.Helper()
	entity, err := store.ResolveEntity(context.Background(), bankID, name, EntityKindRegular, false)
	if err != nil || entity.ID == "" {
		t.Fatalf("resolve entity %q: %v (id %q)", name, err, entity.ID)
	}
	return entity.ID
}

// test-postgres-entity-cooccurrences: the entity graph's rows are maintained on
// the real backend the same way the in-memory one maintains them — an edit
// records the pairs its resulting set witnesses, and the entity prune drops a
// pair no unit witnesses any more, which is the stale case the foreign-key
// cascade cannot reach.
func TestPostgresEntityCooccurrenceMaintenance(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	seedPGCooccurrenceUnit(t, store, "cooc-bank", "00000000-0000-4000-8000-000000000001")
	seedPGCooccurrenceUnit(t, store, "cooc-bank", "00000000-0000-4000-8000-000000000002")

	if err := store.SetUnitEntities(ctx, "cooc-bank", "00000000-0000-4000-8000-000000000001", []string{"Alice", "Berlin"}, true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := store.SetUnitEntities(ctx, "cooc-bank", "00000000-0000-4000-8000-000000000002", []string{"Alice", "Berlin"}, true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	graph, err := store.EntityGraph(ctx, EntityGraphQuery{BankID: "cooc-bank", Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("after the edits the graph = %d edge(s), want the one pair the set witnesses", len(graph.Edges))
	}
	if graph.Edges[0].Weight != 2 {
		t.Fatalf("edge weight = %d, want 2: both units witness the pair", graph.Edges[0].Weight)
	}

	// The second unit stops naming Berlin, leaving the pair unwitnessed while
	// both entities keep a posting.
	if err := store.SetUnitEntities(ctx, "cooc-bank", "00000000-0000-4000-8000-000000000002", []string{"Alice"}, true); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := store.SetUnitEntities(ctx, "cooc-bank", "00000000-0000-4000-8000-000000000001", []string{"Berlin"}, true); err != nil {
		t.Fatalf("edit: %v", err)
	}

	maintainer, ok := store.(GraphMaintainer)
	if !ok {
		t.Fatal("the PostgreSQL store must back the graph-maintenance boundary")
	}
	if err := maintainer.PruneEntity(ctx, "cooc-bank", pgEntityIDForName(t, store, "cooc-bank", "Alice")); err != nil {
		t.Fatalf("prune: %v", err)
	}
	graph, err = store.EntityGraph(ctx, EntityGraphQuery{BankID: "cooc-bank", Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 0 {
		t.Fatalf("graph keeps %d edge(s) no unit witnesses: %+v", len(graph.Edges), graph.Edges)
	}
}
