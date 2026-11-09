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

// A reference-generated series records objects it no longer has as commented-out
// statements. The table set must follow the executable statements only: a
// commented-out name would otherwise enter the set, and a restore of the
// reference's own backup — which carries only live tables — would refuse it.
func TestPersistentTablesSkipCommentedOutObjects(t *testing.T) {
	series := func() ([]Migration, error) {
		return []Migration{{
			Version: 1,
			SQL: `-- CREATE TABLE legacy_dropped (id uuid);
CREATE TABLE parent (id uuid PRIMARY KEY);
-- CREATE TABLE also_dropped (id uuid);
CREATE TABLE child (id uuid PRIMARY KEY);
ALTER TABLE child ADD CONSTRAINT child_parent_fk
    FOREIGN KEY (parent_id) REFERENCES parent (id);`,
		}}, nil
	}
	tables, err := PersistentTablesFrom(series)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) < 2 || tables[0] != "parent" || tables[1] != "child" {
		t.Fatalf("tables = %v, want the series' own tables first, in order", tables)
	}
	for _, name := range tables {
		if name == "legacy_dropped" || name == "also_dropped" {
			t.Fatalf("commented-out table %q entered the set", name)
		}
	}
}

// A trailing comment after a statement is not part of the statement, so the
// names it mentions stay out of the set too.
func TestPersistentTablesIgnoreTrailingComments(t *testing.T) {
	series := func() ([]Migration, error) {
		return []Migration{{Version: 1, SQL: "CREATE TABLE live (id uuid); -- CREATE TABLE ghost (id uuid);"}}, nil
	}
	tables, err := PersistentTablesFrom(series)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 || tables[0] != "live" {
		t.Fatalf("tables = %v, want live first", tables)
	}
	for _, name := range tables {
		if name == "ghost" {
			t.Fatal("a trailing comment's table entered the set")
		}
	}
}
