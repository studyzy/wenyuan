package storage

import "testing"

// The persistent table set is derived from the migrations, not a second list:
// every memory-domain table is covered, and no table is invented.
func TestPersistentTablesCoverTheDomain(t *testing.T) {
	tables, err := PersistentTables()
	if err != nil {
		t.Fatalf("persistent tables: %v", err)
	}
	seen := map[string]bool{}
	for _, name := range tables {
		if seen[name] {
			t.Fatalf("table %q appears twice", name)
		}
		seen[name] = true
	}
	for _, name := range MemoryDomainTables {
		if !seen[name] {
			t.Fatalf("persistent table set is missing %q", name)
		}
	}
	if len(seen) != len(MemoryDomainTables) {
		t.Fatalf("persistent table count = %d, want %d", len(seen), len(MemoryDomainTables))
	}
}

// The derived order places every table after the tables it references, which is
// what restore's forward load depends on.
func TestPersistentTablesAreInDependencyOrder(t *testing.T) {
	tables, err := PersistentTables()
	if err != nil {
		t.Fatal(err)
	}
	position := map[string]int{}
	for i, name := range tables {
		position[name] = i
	}
	for _, edge := range [][2]string{
		{"banks", "bank_aliases"},
		{"banks", "attachments"},
		{"banks", "async_operations"},
		{"banks", "mental_models"},
		{"documents", "chunks"},
		{"documents", "invalidated_memory_units"},
		{"chunks", "memory_units"},
		{"entities", "entity_cooccurrences"},
		{"entities", "memory_links"},
		{"memory_units", "unit_entities"},
		{"mental_models", "mental_model_history"},
		{"mental_models", "knowledge_pages"},
	} {
		parent, ok := position[edge[0]]
		if !ok {
			t.Fatalf("parent table %q is missing", edge[0])
		}
		child, ok := position[edge[1]]
		if !ok {
			t.Fatalf("child table %q is missing", edge[1])
		}
		if parent > child {
			t.Fatalf("table %q is ordered before its parent %q", edge[1], edge[0])
		}
	}
}

// dependencyOrder puts a parent before its children and breaks ties by the
// original position, and it rejects a cycle rather than emitting a partial
// order.
func TestDependencyOrder(t *testing.T) {
	ordered, err := dependencyOrder(
		[]string{"c", "b", "a"},
		map[string][]string{"c": {"a"}, "b": {"a"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0] != "a" {
		t.Fatalf("parent must come first: %v", ordered)
	}
	// A self-reference is not an ordering constraint.
	if _, err := dependencyOrder([]string{"a"}, map[string][]string{"a": {"a"}}); err != nil {
		t.Fatalf("a self-reference must not be a cycle: %v", err)
	}
	if _, err := dependencyOrder([]string{"a", "b"}, map[string][]string{"a": {"b"}, "b": {"a"}}); err == nil {
		t.Fatal("a cycle must be reported")
	}
}
