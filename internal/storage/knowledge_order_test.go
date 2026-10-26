package storage

import (
	"context"
	"testing"
)

// Knowledge tree order is sort_order then name — and nothing else.
//
// The reference sorts its fetched rows in Python with
// `_knowledge_tree_sort_key` (`(sort_order is None, sort_order or 0, name or
// "")`, engine/memory_engine.py:1945-1949) over a query that declares no ORDER
// BY, so identically placed siblings fall back on name and, for equal names, on
// the stable sort's fetch order. This service added an identifier as a third
// key, which put siblings in `kf-<hex>` order instead of name order whenever the
// two disagreed — the tree rendered `F14423, F19177, …` where the reference
// rendered `F12276, F13492, …`, and the difference is invisible only as long as
// the generated identifiers happen to ascend with the names.
func TestKnowledgeNodesOrderBySortOrderThenName(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")

	// Names chosen so name order and identifier order disagree: the "Z" folder
	// carries the lowest identifier, so a third identifier key would put it
	// first.
	for _, node := range []KnowledgeNode{
		{BankID: "b1", ID: "kf-00000000000000000000000000000001", Title: "Zulu", Kind: "folder"},
		{BankID: "b1", ID: "kf-00000000000000000000000000000002", Title: "Alpha", Kind: "folder"},
		{BankID: "b1", ID: "kf-00000000000000000000000000000003", Title: "Mike", Kind: "folder"},
	} {
		if _, err := store.PutKnowledgeNode(ctx, node); err != nil {
			t.Fatalf("put %s: %v", node.Title, err)
		}
	}

	nodes, err := store.ListKnowledgeNodes(ctx, "b1")
	if err != nil {
		t.Fatalf("list knowledge nodes: %v", err)
	}
	got := make([]string, 0, len(nodes))
	for _, node := range nodes {
		got = append(got, node.Title)
	}
	want := []string{"Alpha", "Mike", "Zulu"}
	if len(got) != len(want) {
		t.Fatalf("titles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("titles = %v, want %v (name order, not identifier order)", got, want)
		}
	}
}

// A set sort_order still leads: the name only breaks ties among equal orders.
func TestKnowledgeNodesOrderPutsSortOrderFirst(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")

	three, two := 3, 2
	for _, node := range []KnowledgeNode{
		{BankID: "b1", ID: "kf-00000000000000000000000000000001", Title: "Alpha", Kind: "folder", SortOrder: three},
		{BankID: "b1", ID: "kf-00000000000000000000000000000002", Title: "Zulu", Kind: "folder", SortOrder: two},
		{BankID: "b1", ID: "kf-00000000000000000000000000000003", Title: "Mike", Kind: "folder"},
	} {
		if _, err := store.PutKnowledgeNode(ctx, node); err != nil {
			t.Fatalf("put %s: %v", node.Title, err)
		}
	}

	nodes, err := store.ListKnowledgeNodes(ctx, "b1")
	if err != nil {
		t.Fatalf("list knowledge nodes: %v", err)
	}
	got := make([]string, 0, len(nodes))
	for _, node := range nodes {
		got = append(got, node.Title)
	}
	// The unset sort_order is the zero for ordering purposes (`sort_order or 0`
	// in the reference), so it leads this set rather than trailing it.
	want := []string{"Mike", "Zulu", "Alpha"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("titles = %v, want %v", got, want)
		}
	}
}
