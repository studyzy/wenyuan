package storage

import (
	"context"
	"testing"
	"time"
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

// A tag-filtered entity read must see what an observation inherits: the
// reference's tag-filtered entity rowset UNIONs the rows an observation with no
// postings of its own inherits from its source memories
// (engine/memories/pg/curation.py:539-583), so the list, the detail and the
// entity graph all count them. Without it, an entity only observations mention
// vanishes under a filter and the counts of the entities it shares undercount.
func TestMemoryTagFilteredEntityReadsInheritObservationMentions(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	// The source memory names Alice and Berlin and carries no tag of its own;
	// only the observation that inherited both mentions matches the filter.
	seedCooccurrenceUnit(t, store, "b1", "src")
	if err := store.SetUnitEntities(ctx, "b1", "src", []string{"Alice", "Berlin"}, true); err != nil {
		t.Fatalf("seed source entities: %v", err)
	}
	seen := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	store.mu.Lock()
	store.units["obs"] = MemoryUnit{
		ID: "obs", BankID: "b1", FactType: TypeObservation, Tags: []string{"ops"},
		SourceMemoryIDs: []string{"src"}, EventDate: &seen, Metadata: map[string]any{},
	}
	store.mu.Unlock()

	alice := store.entityIDForName(t, "b1", "Alice")

	// The list: both inherited mentions count once, over the matching memories.
	page, err := store.ListEntities(ctx, EntityQuery{BankID: "b1", Tags: []string{"ops"}, TagsMatch: TagsMatchAnyStrict, Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if page.Total != 2 || len(page.Entities) != 2 {
		t.Fatalf("filtered list = %d of %d, want both inherited mentions", len(page.Entities), page.Total)
	}
	for _, entity := range page.Entities {
		if entity.MentionCount != 1 {
			t.Fatalf("%s mention_count = %d, want the one inherited mention", entity.CanonicalName, entity.MentionCount)
		}
		if entity.FirstSeen == nil || !entity.FirstSeen.Equal(seen) || entity.LastSeen == nil || !entity.LastSeen.Equal(seen) {
			t.Fatalf("%s span = %v..%v, want the matching memory's date", entity.CanonicalName, entity.FirstSeen, entity.LastSeen)
		}
	}

	// The detail: the same recomputation answers for one entity, so an entity
	// mentioned only through inheritance resolves under its filter.
	detail, err := store.ListEntities(ctx, EntityQuery{BankID: "b1", ID: alice, Tags: []string{"ops"}, TagsMatch: TagsMatchAnyStrict, Limit: 10})
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Total != 1 || len(detail.Entities) != 1 || detail.Entities[0].MentionCount != 1 {
		t.Fatalf("filtered detail = %+v, want Alice with one inherited mention", detail.Entities)
	}

	// The entity graph: the pair the matching memory witnesses through its
	// inherited mentions is an edge with both endpoints' counts.
	graph, err := store.EntityGraph(ctx, EntityGraphQuery{BankID: "b1", Tags: []string{"ops"}, TagsMatch: TagsMatchAnyStrict, Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 1 || graph.Edges[0].Weight != 1 {
		t.Fatalf("filtered graph = %d edge(s) %+v, want the one pair the observation witnesses", len(graph.Edges), graph.Edges)
	}
	if len(graph.Nodes) != 2 {
		t.Fatalf("filtered graph nodes = %d, want both endpoints", len(graph.Nodes))
	}
	for _, node := range graph.Nodes {
		if node.MentionCount != 1 {
			t.Fatalf("%s node mention_count = %d, want 1", node.Label, node.MentionCount)
		}
	}
}

// The entity graph's nodes follow edge-appearance order: walking the edges
// strongest first, each contributes its endpoints the first time they are seen
// (engine/memories/pg/engine_curation.py:417-424). The postgres backend already
// renders them that way; the memory backend must not sort them by mention count
// instead.
func TestMemoryEntityGraphNodesFollowEdgeAppearanceOrder(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	// The pair identities are pinned so the expected order is deterministic:
	// u1 witnesses Alice-Berlin twice and u2 witnesses Berlin-Carol once, so
	// the strongest edge names Alice first. By mention count Berlin (3) would
	// lead instead.
	seedCooccurrenceUnit(t, store, "b1", "u1")
	seedCooccurrenceUnit(t, store, "b1", "u2")
	alice, berlin, carol := "e-alice", "e-berlin", "e-carol"
	store.mu.Lock()
	store.entities[alice] = Entity{ID: alice, BankID: "b1", CanonicalName: "Alice", MentionCount: 2}
	store.entities[berlin] = Entity{ID: berlin, BankID: "b1", CanonicalName: "Berlin", MentionCount: 3}
	store.entities[carol] = Entity{ID: carol, BankID: "b1", CanonicalName: "Carol", MentionCount: 1}
	store.unitEntities["u1"] = []string{alice, berlin}
	store.unitEntities["u2"] = []string{berlin, carol}
	store.cooccurrences["b1\x00"+alice+"\x00"+berlin] = 2
	store.cooccurrences["b1\x00"+berlin+"\x00"+carol] = 1
	store.mu.Unlock()

	graph, err := store.EntityGraph(ctx, EntityGraphQuery{BankID: "b1", Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 2 || graph.Edges[0].Weight != 2 || graph.Edges[1].Weight != 1 {
		t.Fatalf("edges = %+v, want Alice-Berlin (2) ahead of Berlin-Carol (1)", graph.Edges)
	}
	want := []string{"Alice", "Berlin", "Carol"}
	if len(graph.Nodes) != len(want) {
		t.Fatalf("nodes = %+v, want %v", graph.Nodes, want)
	}
	for i, node := range graph.Nodes {
		if node.Label != want[i] {
			t.Fatalf("node %d = %q, want %q: nodes follow edge-appearance order", i, node.Label, want[i])
		}
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
