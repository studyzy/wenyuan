//go:build pgx

package storage

import (
	"context"
	"testing"
)

// An edit on PostgreSQL drops the derived links incident to the edited unit in
// both directions, while the causal edges survive — the reference's apply_edit
// sweeps the derived edges only (#2864).
func TestPostgresEditDropsNonCausalLinks(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	bank := "link-drop-bank"

	content := "lost the job, then could not pay rent, then moved out"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {
			{Text: "They lost the job."},
			{Text: "They could not pay rent.", CausedByIndexes: []int{0}},
			{Text: "They moved out.", CausedByIndexes: []int{1}},
		},
	}}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID:     bank,
		Items:      []RetainItem{{Content: content, DocumentID: "link-drop-doc"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	units := outcome.ItemUnitIDs[0]
	if len(units) != 3 {
		t.Fatalf("stored %d units, want 3", len(units))
	}

	newText := "a materially different fact"
	if _, err := store.UpdateMemory(ctx, bank, units[1], MemoryPatch{Text: &newText}); err != nil {
		t.Fatal(err)
	}

	graph, err := store.Graph(ctx, GraphQuery{BankID: bank, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	hasEdge := func(from, to, kind string) bool {
		for _, edge := range graph.Edges {
			if edge.From == from && edge.To == to && edge.Type == kind {
				return true
			}
		}
		return false
	}
	if hasEdge(units[0], units[1], string(LinkTemporal)) {
		t.Fatal("edit left the incoming temporal edge in place")
	}
	if hasEdge(units[1], units[2], string(LinkTemporal)) {
		t.Fatal("edit left the outgoing temporal edge in place")
	}
	if !hasEdge(units[1], units[0], string(LinkCausedBy)) {
		t.Fatal("edit dropped an outgoing causal edge")
	}
	if !hasEdge(units[2], units[1], string(LinkCausedBy)) {
		t.Fatal("edit dropped an incoming causal edge")
	}
}

// The graph-maintenance pass re-derives the temporal edge an edit dropped: the
// edit queues the edited unit and its victims, and the drain relinks them. The
// edge stays gone without the temporal top-up, which is what makes this fail on
// a semantic-only relink.
func TestPostgresGraphMaintenanceReDerivesEditedTemporalLinks(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	bank := "link-relink-bank"

	content := "lost the job, then could not pay rent, then moved out"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {
			{Text: "They lost the job."},
			{Text: "They could not pay rent.", CausedByIndexes: []int{0}},
			{Text: "They moved out.", CausedByIndexes: []int{1}},
		},
	}}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID:     bank,
		Items:      []RetainItem{{Content: content, DocumentID: "link-relink-doc"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	units := outcome.ItemUnitIDs[0]
	if len(units) != 3 {
		t.Fatalf("stored %d units, want 3", len(units))
	}

	newText := "a materially different fact"
	if _, err := store.UpdateMemory(ctx, bank, units[1], MemoryPatch{Text: &newText}); err != nil {
		t.Fatal(err)
	}

	maintainer, ok := store.(GraphMaintainer)
	if !ok {
		t.Fatal("the postgres store is not a graph maintainer")
	}
	targets, err := maintainer.DrainGraphMaintenance(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("the edit queued no relink victims")
	}
	for _, target := range targets {
		if err := maintainer.RelinkUnit(ctx, target.BankID, target.ID); err != nil {
			t.Fatal(err)
		}
	}

	graph, err := store.Graph(ctx, GraphQuery{BankID: bank, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	hasEdge := func(from, to, kind string) bool {
		for _, edge := range graph.Edges {
			if edge.From == from && edge.To == to && edge.Type == kind {
				return true
			}
		}
		return false
	}
	if !hasEdge(units[1], units[2], string(LinkTemporal)) {
		t.Fatal("relink did not re-derive the dropped outgoing temporal edge")
	}
	if !hasEdge(units[0], units[1], string(LinkTemporal)) {
		t.Fatal("relink did not re-derive the dropped incoming temporal edge")
	}
	if !hasEdge(units[1], units[0], string(LinkCausedBy)) {
		t.Fatal("relink dropped an outgoing causal edge")
	}
	if !hasEdge(units[2], units[1], string(LinkCausedBy)) {
		t.Fatal("relink dropped an incoming causal edge")
	}
}
