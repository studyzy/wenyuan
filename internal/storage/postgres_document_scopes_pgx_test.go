//go:build pgx

package storage

import (
	"context"
	"testing"
)

// The PostgreSQL document listing takes its `q` over the id and the stored text,
// escaping LIKE's wildcards so a literal `%` or `_` is that character.
func TestPostgresDocumentSearchMatchesIDAndText(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()
	docs := map[string]string{
		"report-2024": "quarterly numbers",
		"notes":       "the Report landed late",
		"percent":     "100% done",
		"other":       "nothing here",
	}
	for id, text := range docs {
		if _, err := store.PutDocument(ctx, "read-bank", id, Document{OriginalText: text}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListDocuments(ctx, DocumentQuery{BankID: "read-bank", Query: "REPORT", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.TotalIsEstimate {
		t.Fatalf("q=REPORT total = %d estimate = %v, want 2 false", page.Total, page.TotalIsEstimate)
	}
	fields := map[string][]string{}
	for _, doc := range page.Documents {
		fields[doc.ID] = doc.MatchedFields
	}
	if len(fields["report-2024"]) != 1 || fields["report-2024"][0] != "id" {
		t.Fatalf("id match fields = %v", fields["report-2024"])
	}
	if len(fields["notes"]) != 1 || fields["notes"][0] != "original_text" {
		t.Fatalf("text match fields = %v", fields["notes"])
	}
	// "%" matches itself: only the document that literally holds one.
	literal, err := store.ListDocuments(ctx, DocumentQuery{BankID: "read-bank", Query: "%", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if literal.Total != 1 || literal.Documents[0].ID != "percent" {
		t.Fatalf("q=%% total = %d: %+v", literal.Total, literal.Documents)
	}
}

// A document update that only changes the observation scoping writes the new
// scope onto every memory it moves, requeues them, and leaves the tags alone —
// and one that resolves to the scopes the memories already write to moves none
// of them.
func TestPostgresRetagMovesOnlyScopesThatChange(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()
	if _, err := store.PutDocument(ctx, "read-bank", "doc-1", Document{
		OriginalText: "text", Tags: []string{"session:a"},
	}); err != nil {
		t.Fatal(err)
	}
	factID := pgUUID("fact-1")
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: factID, BankID: "read-bank", Text: "a fact", DocumentID: "doc-1",
		FactType: TypeWorld, Tags: []string{"session:a"},
	}); err != nil {
		t.Fatal(err)
	}

	outcome, err := store.RetagDocumentMemories(ctx, "read-bank", "doc-1", DocumentScopeChange{
		OldTags: []string{"session:a"}, NewScopes: []byte(`"shared"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.RequeuedUnits != 1 {
		t.Fatalf("requeued = %d, want 1", outcome.RequeuedUnits)
	}
	unit, err := store.GetMemory(ctx, "read-bank", factID)
	if err != nil {
		t.Fatal(err)
	}
	if string(unit.ObservationScopes) != `"shared"` {
		t.Fatalf("unit scopes = %s, want \"shared\"", unit.ObservationScopes)
	}
	if len(unit.Tags) != 1 || unit.Tags[0] != "session:a" {
		t.Fatalf("unit tags = %v, want [session:a] (a scope-only update writes no tags)", unit.Tags)
	}
	if unit.ConsolidatedAt != nil {
		t.Fatalf("moved unit still consolidated at %v", unit.ConsolidatedAt)
	}

	// Re-sending the same scoping moves nothing.
	outcome, err = store.RetagDocumentMemories(ctx, "read-bank", "doc-1", DocumentScopeChange{
		OldTags: []string{"session:a"}, NewScopes: []byte(`"shared"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.RequeuedUnits != 0 || outcome.InvalidatedObservations != 0 {
		t.Fatalf("a no-op update moved %+v", outcome)
	}
}
