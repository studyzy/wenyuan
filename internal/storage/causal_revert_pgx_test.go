//go:build pgx

package storage

import (
	"context"
	"testing"
)

// Invalidation parks a causal edge on the archive row of every endpoint, and a
// revert rebuilds it once both endpoints are live again. Edges whose peer is
// still archived stay unmaterialized until that peer reverts, so the order of
// the two reverts does not matter.
func TestPostgresCausalLinksSurviveInvalidateRevert(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	bank := "causal-revert-bank"
	content := "lost the job, then could not pay rent"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {
			{Text: "They lost the job."},
			{Text: "They could not pay rent.", CausedByIndexes: []int{0}},
		},
	}}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID:     bank,
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	units := outcome.ItemUnitIDs[0]
	if len(units) != 2 {
		t.Fatalf("stored %d units, want 2", len(units))
	}
	cause, effect := units[0], units[1]

	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatal("the test store is not a PostgresStore")
	}
	schema, err := pg.schemaFor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	archiveQuery := "SELECT COALESCE(causal_links, '[]'::jsonb) FROM " + quoteIdent(schema) +
		".invalidated_memory_units WHERE bank_id = $1 AND id = $2"
	archivedDescriptors := func(unitID string) []causalDescriptor {
		var raw []byte
		if err := pg.reader().QueryRowContext(ctx, archiveQuery, bank, unitID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return parseCausalDescriptors(raw)
	}
	hasCausalEdge := func() bool {
		graph, err := store.Graph(ctx, GraphQuery{BankID: bank, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, edge := range graph.Edges {
			if edge.Type == string(LinkCausedBy) && edge.From == effect && edge.To == cause {
				return true
			}
		}
		return false
	}

	invalid := "invalidated"
	if _, err := store.UpdateMemory(ctx, bank, cause, MemoryPatch{State: &invalid}); err != nil {
		t.Fatal(err)
	}
	if got := archivedDescriptors(cause); len(got) != 1 {
		t.Fatalf("the cause's archive row holds %d descriptors, want 1: %+v", len(got), got)
	}
	// The live edge is gone with the cause, but the effect's own invalidation
	// must still find it through the archived peer's descriptor.
	if _, err := store.UpdateMemory(ctx, bank, effect, MemoryPatch{State: &invalid}); err != nil {
		t.Fatal(err)
	}
	if got := archivedDescriptors(effect); len(got) != 1 {
		t.Fatalf("the effect's archive row holds %d descriptors, want 1: %+v", len(got), got)
	}

	valid := "valid"
	if _, err := store.UpdateMemory(ctx, bank, cause, MemoryPatch{State: &valid}); err != nil {
		t.Fatal(err)
	}
	if hasCausalEdge() {
		t.Fatal("an edge whose peer is still archived must not be rebuilt")
	}
	if _, err := store.UpdateMemory(ctx, bank, effect, MemoryPatch{State: &valid}); err != nil {
		t.Fatal(err)
	}
	if !hasCausalEdge() {
		t.Fatal("the causal edge was not rebuilt once both endpoints were live")
	}

	// A second cycle is idempotent: ON CONFLICT DO NOTHING keeps one edge.
	if _, err := store.UpdateMemory(ctx, bank, cause, MemoryPatch{State: &invalid}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateMemory(ctx, bank, cause, MemoryPatch{State: &valid}); err != nil {
		t.Fatal(err)
	}
	graph, err := store.Graph(ctx, GraphQuery{BankID: bank, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, edge := range graph.Edges {
		if edge.Type == string(LinkCausedBy) && edge.From == effect && edge.To == cause {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("repeated cycles left %d causal edges, want 1", count)
	}
}
