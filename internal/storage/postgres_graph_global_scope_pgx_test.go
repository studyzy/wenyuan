//go:build pgx

package storage

import (
	"context"
	"testing"
)

// `tags_match=exact` with no tags selects the untagged "global" scope, not
// "no filter": the reference emits `(tags IS NULL OR tags = '{}')` for it
// (engine/memories/pg/graph.py:219-236), and the other modes keep treating empty
// tags as no filter. Pinned on the SQL backend because that is the one whose
// predicate is rendered rather than evaluated.
func TestPostgresGraphExactWithNoTagsSelectsTheGlobalScope(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	const bank = "w15-graph-exact-global"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	global := MemoryUnit{ID: newID(), BankID: bank, Text: "obs global", FactType: TypeObservation}
	tagged := MemoryUnit{ID: newID(), BankID: bank, Text: "obs tagged", FactType: TypeObservation, Tags: []string{"work"}}
	for _, unit := range []MemoryUnit{global, tagged} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatalf("upsert unit: %v", err)
		}
	}
	texts := func(query GraphQuery) map[string]bool {
		t.Helper()
		query.BankID = bank
		query.Type = string(TypeObservation)
		graph, err := store.Graph(ctx, query)
		if err != nil {
			t.Fatalf("graph: %v", err)
		}
		got := map[string]bool{}
		for _, row := range graph.TableRows {
			got[row.Text] = true
		}
		return got
	}

	exact := texts(GraphQuery{TagsMatch: TagsMatchExact, Limit: 100})
	if len(exact) != 1 || !exact["obs global"] {
		t.Fatalf("exact with no tags = %v, want only the untagged observation", exact)
	}
	for _, mode := range []string{TagsMatchAny, TagsMatchAll, TagsMatchAnyStrict, TagsMatchAllStrict} {
		if got := texts(GraphQuery{TagsMatch: mode, Limit: 100}); len(got) != 2 {
			t.Fatalf("tags_match=%s with no tags = %v, want the whole bank", mode, got)
		}
	}
	if got := texts(GraphQuery{Tags: []string{"work"}, TagsMatch: TagsMatchExact, Limit: 100}); len(got) != 1 || !got["obs tagged"] {
		t.Fatalf("exact with tags = %v, want only the tagged observation", got)
	}
}
