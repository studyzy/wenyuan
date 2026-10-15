//go:build pgx

package storage

import (
	"context"
	"testing"
)

// A retain write against PostgreSQL persists the causal edges extraction
// declared, in the effect-to-cause direction, so the graph read surface can
// walk them.
func TestPostgresRetainPersistsCausalLinks(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	content := "lost the job, then could not pay rent"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {
			{Text: "They lost the job."},
			{Text: "They could not pay rent.", CausedByIndexes: []int{0}},
		},
	}}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID:     "vec-bank",
		Items:      []RetainItem{{Content: content, DocumentID: "causal-doc"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	units := outcome.ItemUnitIDs[0]
	if len(units) != 2 {
		t.Fatalf("stored %d units, want 2", len(units))
	}

	graph, err := store.Graph(ctx, GraphQuery{BankID: "vec-bank", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var causal []GraphEdgeData
	for _, edge := range graph.Edges {
		if edge.Type == string(LinkCausedBy) {
			causal = append(causal, edge)
		}
	}
	if len(causal) != 1 {
		t.Fatalf("caused_by edges = %d, want 1: %+v", len(causal), graph.Edges)
	}
	if causal[0].From != units[1] || causal[0].To != units[0] {
		t.Fatalf("edge = %s -> %s, want effect %s -> cause %s", causal[0].From, causal[0].To, units[1], units[0])
	}
}
