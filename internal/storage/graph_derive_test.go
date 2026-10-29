package storage

import (
	"context"
	"fmt"
	"testing"
)

// The graph view's edge assembly stays bounded on a densely-connected bank:
// the reference gives the stored links and the derived edges one budget of
// GRAPH_MAX_EDGES each (tests/test_graph_assembly.py), so a dense bank renders
// exactly the stored edges plus a full derived budget rather than every edge
// it can derive.
func TestMemoryGraphDerivedEdgesStopAtTheCap(t *testing.T) {
	store := NewMemoryStore("public")
	store.mu.Lock()
	// 100 source memories naming the same five entities, and 1000 observations
	// consolidating two of them each: uncapped, the semantic pairs alone exceed
	// one budget, and the entity edges would follow them.
	entityIDs := make([]string, 5)
	for i := range entityIDs {
		entityIDs[i] = fmt.Sprintf("entity-%d", i)
		store.entities[entityIDs[i]] = Entity{ID: entityIDs[i], BankID: "b1", CanonicalName: entityIDs[i]}
	}
	for s := range 100 {
		sourceID := fmt.Sprintf("src-%03d", s)
		store.units[sourceID] = MemoryUnit{ID: sourceID, BankID: "b1", FactType: TypeWorld}
		store.unitEntities[sourceID] = entityIDs
	}
	for i := range 1000 {
		obsID := fmt.Sprintf("obs-%04d", i)
		store.units[obsID] = MemoryUnit{
			ID: obsID, BankID: "b1", FactType: TypeObservation,
			SourceMemoryIDs: []string{fmt.Sprintf("src-%03d", i%100), fmt.Sprintf("src-%03d", (i*7)%100)},
		}
	}
	store.links = append(store.links, MemoryLink{
		BankID: "b1", FromUnitID: "obs-0000", ToUnitID: "obs-0001", LinkType: LinkTemporal, Weight: 0.5,
	})
	store.mu.Unlock()

	graph, err := store.Graph(context.TODO(), GraphQuery{BankID: "b1", Limit: 1100})
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	// The stored link, then a full budget of derived edges — never every edge
	// the bank can derive.
	if len(graph.Edges) != 1+graphEdgeCap {
		t.Fatalf("dense bank rendered %d edges, want 1 + %d", len(graph.Edges), graphEdgeCap)
	}
	if graph.Edges[0].Type != string(LinkTemporal) {
		t.Fatalf("first edge = %q, want the stored temporal link", graph.Edges[0].Type)
	}
	// The derived budget is filled by the semantic edges because they are
	// derived first (engine/memory_engine.py:2691-2694 chains semantic_links
	// ahead of entity_links): no entity edge survives the cap here.
	semantic := 0
	for _, edge := range graph.Edges[1:] {
		if edge.Type == string(LinkSemantic) {
			semantic++
		}
	}
	if semantic != graphEdgeCap {
		t.Fatalf("derived edges held %d semantic edges, want a full budget of them", semantic)
	}
	if len(graph.Nodes) != 1100 {
		t.Fatalf("graph rendered %d nodes, want every visible unit", len(graph.Nodes))
	}
}

// A bank whose stored links and the copies observations inherit overflow one
// budget still renders derived edges: the two buckets each get their own
// budget, so the copies do not eat the entity edges' room
// (tests/test_graph_assembly.py).
func TestMemoryGraphStoredLinksLeaveRoomForDerivedEdges(t *testing.T) {
	store := NewMemoryStore("public")
	store.mu.Lock()
	for i := range 100 {
		id := fmt.Sprintf("u-%03d", i)
		store.units[id] = MemoryUnit{ID: id, BankID: "b1", FactType: TypeWorld}
		store.unitEntities[id] = []string{"acme"}
		// One observation per source keeps the derived set to entity edges:
		// two observations on one source would derive semantic pairs too.
		obsID := fmt.Sprintf("obs-%03d", i)
		store.units[obsID] = MemoryUnit{
			ID: obsID, BankID: "b1", FactType: TypeObservation, SourceMemoryIDs: []string{id},
		}
	}
	store.entities["acme"] = Entity{ID: "acme", BankID: "b1", CanonicalName: "Acme"}
	// Every ordered pair of world units carries a stored semantic link, and
	// each of those links copies onto both endpoints' observations: 4950 direct
	// rows and 9900 copies, well past one budget together.
	for a := range 100 {
		for b := a + 1; b < 100; b++ {
			store.links = append(store.links, MemoryLink{
				BankID: "b1", FromUnitID: fmt.Sprintf("u-%03d", a), ToUnitID: fmt.Sprintf("u-%03d", b),
				LinkType: LinkSemantic, Weight: 0.9,
			})
		}
	}
	store.mu.Unlock()

	graph, err := store.Graph(context.TODO(), GraphQuery{BankID: "b1", Limit: 200})
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	stored, derived := 0, 0
	for _, edge := range graph.Edges {
		switch edge.Type {
		case string(LinkSemantic):
			stored++
		case string(LinkEntity):
			derived++
		}
	}
	if stored != graphEdgeCap {
		t.Fatalf("direct and copied links filled %d edges, want a full budget", stored)
	}
	if derived == 0 {
		t.Fatal("a full stored budget must leave room for entity edges")
	}
}

// Derived semantic edges come before derived entity edges: the reference chains
// semantic_links() ahead of entity_links() (engine/memory_engine.py:2691-2694),
// which decides which derived edges a full budget keeps. The callers hand the
// entity map already inherited, so the two observations name Alice through
// their source.
func TestGraphDerivedEdgesSemanticBeforeEntity(t *testing.T) {
	units := []GraphNodeData{
		{ID: "obs-a", FactType: string(TypeObservation), SourceMemoryIDs: []string{"src"}, Entities: []string{"Alice"}},
		{ID: "obs-b", FactType: string(TypeObservation), SourceMemoryIDs: []string{"src"}, Entities: []string{"Alice"}},
	}
	entities := map[string][]string{"obs-a": {"Alice"}, "obs-b": {"Alice"}}
	edges := deriveGraphEdges(units, entities, nil)
	if len(edges) != 2 {
		t.Fatalf("edges = %+v, want the one semantic pair and the one entity pair", edges)
	}
	if edges[0].Type != string(LinkSemantic) {
		t.Fatalf("first derived edge = %q, want semantic", edges[0].Type)
	}
	if edges[1].Type != string(LinkEntity) {
		t.Fatalf("second derived edge = %q, want entity", edges[1].Type)
	}
}

// Entity edge groups are walked ascending by unit count, ties in
// first-appearance order (engine/memory_engine.py:2679 sorts the same way): a
// name in every memory links everything to everything and says nothing, so its
// edges are the first a budget drops.
func TestGraphDerivedEntityGroupsSmallestFirst(t *testing.T) {
	units := []GraphNodeData{
		{ID: "u1", FactType: string(TypeWorld), Entities: []string{"Alpha", "Beta"}},
		{ID: "u2", FactType: string(TypeWorld), Entities: []string{"Alpha", "Beta"}},
		{ID: "u3", FactType: string(TypeWorld), Entities: []string{"Alpha"}},
	}
	entities := map[string][]string{
		"u1": {"Alpha", "Beta"}, "u2": {"Alpha", "Beta"}, "u3": {"Alpha"},
	}
	edges := deriveGraphEdges(units, entities, nil)
	if len(edges) != 4 {
		t.Fatalf("edges = %+v, want the three Alpha pairs and the one Beta pair", edges)
	}
	// Beta names fewer units, so its pair is emitted ahead of Alpha's even
	// though Alpha appeared first.
	if edges[0].Entity != "Beta" || edges[0].From != "u1" || edges[0].To != "u2" {
		t.Fatalf("first edge = %+v, want the Beta pair u1-u2", edges[0])
	}
	if edges[1].Entity != "Alpha" || edges[1].From != "u1" || edges[1].To != "u2" {
		t.Fatalf("second edge = %+v, want the Alpha pair u1-u2", edges[1])
	}
}
