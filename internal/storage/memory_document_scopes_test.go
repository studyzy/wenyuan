package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// A document write stamps the row: the reference defaults updated_at to the
// write's clock on insert and re-stamps it beside the fields every update sets
// (documents.py:386-400).
func TestPutDocumentStampsUpdatedAt(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.PutDocument(ctx, "b1", "doc-1", Document{OriginalText: "text", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	doc, err := store.GetDocument(ctx, "b1", "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.UpdatedAt.IsZero() {
		t.Fatal("insert left the documents row unstamped")
	}
	if !doc.CreatedAt.Equal(created) {
		t.Fatalf("created_at = %v, want the caller's %v", doc.CreatedAt, created)
	}
	// A re-write re-stamps the row with the last write's clock.
	first := doc.UpdatedAt
	if _, err := store.PutDocument(ctx, "b1", "doc-1", doc); err != nil {
		t.Fatal(err)
	}
	doc, err = store.GetDocument(ctx, "b1", "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.UpdatedAt.Before(first) {
		t.Fatalf("updated_at = %v, want at least the previous write's %v", doc.UpdatedAt, first)
	}
}

// A retag stamps the units it writes: a tag change stamps every unit of the
// document, a scope change alone stamps only the units that moved, and a unit
// whose scopes stay put keeps its stamp (documents.py:486-498).
func TestRetagDocumentMemoriesStampsUpdatedAt(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	written := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	units := []struct {
		id     string
		scopes []byte
	}{
		// combined over [session:a] moves onto shared; the unit already on
		// shared stays where it was.
		{id: "moved"},
		{id: "kept", scopes: []byte(`"shared"`)},
	}
	for _, unit := range units {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: unit.id, BankID: "b1", Text: "a fact", DocumentID: "doc-1",
			FactType: TypeWorld, Tags: []string{"session:a"},
			ObservationScopes: unit.scopes, CreatedAt: &written, UpdatedAt: &written,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := store.RetagDocumentMemories(ctx, "b1", "doc-1", DocumentScopeChange{
		OldTags: []string{"session:a"}, NewScopes: []byte(`"shared"`),
	}); err != nil {
		t.Fatal(err)
	}
	moved, err := store.GetMemory(ctx, "b1", "moved")
	if err != nil {
		t.Fatal(err)
	}
	if moved.UpdatedAt == nil || moved.UpdatedAt.Equal(written) {
		t.Fatalf("moved unit updated_at = %v, want re-stamped", moved.UpdatedAt)
	}
	kept, err := store.GetMemory(ctx, "b1", "kept")
	if err != nil {
		t.Fatal(err)
	}
	if kept.UpdatedAt == nil || !kept.UpdatedAt.Equal(written) {
		t.Fatalf("kept unit updated_at = %v, want untouched %v", kept.UpdatedAt, written)
	}

	// A tag change stamps every unit of the document, not only the moved ones.
	if _, err := store.RetagDocumentMemories(ctx, "b1", "doc-1", DocumentScopeChange{
		OldTags: []string{"session:a"}, NewTags: []string{"team-a"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"moved", "kept"} {
		unit, err := store.GetMemory(ctx, "b1", id)
		if err != nil {
			t.Fatal(err)
		}
		if unit.UpdatedAt != nil && !unit.UpdatedAt.After(written) {
			t.Fatalf("%s updated_at = %v, want stamped past %v", id, unit.UpdatedAt, written)
		}
		if len(unit.Tags) != 1 || unit.Tags[0] != "team-a" {
			t.Fatalf("%s tags = %v, want [team-a]", id, unit.Tags)
		}
	}
}

// writeScopesFor is the storage-side mirror of the consolidator's write-scope
// resolution; the two are pinned together by a cross-package parity test outside
// this package. This table fixes the shapes it must resolve.
func TestWriteScopesForResolvesEveryScopeSpec(t *testing.T) {
	tests := []struct {
		name string
		tags []string
		raw  string
		want [][]string
	}{
		{name: "unset is combined", tags: []string{"b", "a"}, want: [][]string{{"a", "b"}}},
		{name: "combined", tags: []string{"a"}, raw: `"combined"`, want: [][]string{{"a"}}},
		{name: "shared is untagged", tags: []string{"a"}, raw: `"shared"`, want: [][]string{{}}},
		{name: "per_tag", tags: []string{"a", "b"}, raw: `"per_tag"`, want: [][]string{{"a"}, {"b"}}},
		{name: "per_tag untagged", tags: nil, raw: `"per_tag"`, want: [][]string{{}}},
		{
			name: "all_combinations", tags: []string{"a", "b"}, raw: `"all_combinations"`,
			want: [][]string{{"a"}, {"a", "b"}, {"b"}},
		},
		{name: "explicit", tags: []string{"a", "b"}, raw: `[["a"],["b","c"]]`, want: [][]string{{"a"}, {"b", "c"}}},
		{name: "explicit empty scope is kept", tags: []string{"a"}, raw: `[[],["a"]]`, want: [][]string{{}, {"a"}}},
		{name: "explicit empty list is combined", tags: []string{"a"}, raw: `[]`, want: [][]string{{"a"}}},
		{
			name: "globs expand against the memory's tags", tags: []string{"topic:go", "topic:sql", "session:1"},
			raw: `[["topic:*"]]`, want: [][]string{{"topic:go", "topic:sql"}},
		},
		{
			name: "a scope whose globs all miss is dropped", tags: []string{"topic:go"},
			raw: `[["nope:*"],["topic:go"]]`, want: [][]string{{"topic:go"}},
		},
		{
			name: "every scope dropped falls back to combined, never untagged", tags: []string{"topic:go"},
			raw: `[["nope:*"]]`, want: [][]string{{"topic:go"}},
		},
		{name: "null is unset", tags: []string{"a"}, raw: `null`, want: [][]string{{"a"}}},
		{name: "unknown mode is unset", tags: []string{"a"}, raw: `"everything"`, want: [][]string{{"a"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := writeScopesFor(test.tags, rawScopes(test.raw))
			if len(got) != len(test.want) {
				t.Fatalf("scopes = %v, want %v", scopeKeys(got), test.want)
			}
			for _, scope := range test.want {
				if !got[scopeKey(canonicalScope(scope))] {
					t.Fatalf("scopes = %v, want %v", scopeKeys(got), test.want)
				}
			}
		})
	}
}

// One tag change and one scoping change move a memory only when the scopes it
// writes to differ (#5593): the same scopes in another spelling rebuild nothing.
func TestDocumentScopesChanged(t *testing.T) {
	tests := []struct {
		name                 string
		oldTags, newTags     string
		oldScopes, newScopes string
		want                 bool
	}{
		{name: "identical", oldTags: `["a"]`, newTags: `["a"]`, want: false},
		{name: "combined tag change moves", oldTags: `["a"]`, newTags: `["b"]`, want: true},
		{name: "shared ignores the tags", oldTags: `["a"]`, newTags: `["b"]`, oldScopes: `"shared"`, newScopes: `"shared"`},
		{
			name: "explicit list ignores the tags", oldTags: `["a"]`, newTags: `["b"]`,
			oldScopes: `[["x"]]`, newScopes: `[["x"]]`,
		},
		{
			name: "combined over one tag equals the same explicit list", oldTags: `["a"]`, newTags: `["a"]`,
			oldScopes: `"combined"`, newScopes: `[["a"]]`,
		},
		{name: "mode change moves", oldTags: `["a"]`, newTags: `["a"]`, oldScopes: `"combined"`, newScopes: `"shared"`, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := documentScopesChanged(tagsOf(test.oldTags), rawScopes(test.oldScopes), tagsOf(test.newTags), rawScopes(test.newScopes))
			if got != test.want {
				t.Fatalf("scopes changed = %v, want %v", got, test.want)
			}
		})
	}
}

// A document listing's q matches the id or the stored text, literally and
// case-insensitively, and reports which fields matched.
func TestDocumentSearchMatches(t *testing.T) {
	tests := []struct {
		name string
		doc  Document
		q    string
		want []string
	}{
		{name: "id", doc: Document{ID: "report-2024", OriginalText: "notes"}, q: "REPORT", want: []string{"id"}},
		{name: "text", doc: Document{ID: "a", OriginalText: "The Report landed"}, q: "report", want: []string{"original_text"}},
		{name: "both, id first", doc: Document{ID: "report", OriginalText: "report"}, q: "rep", want: []string{"id", "original_text"}},
		{name: "no match", doc: Document{ID: "a", OriginalText: "b"}, q: "zzz", want: []string{}},
		{name: "wildcards are literal", doc: Document{ID: "a", OriginalText: "100% sure"}, q: "%", want: []string{"original_text"}},
		{name: "underscore is literal", doc: Document{ID: "a_b", OriginalText: "axb"}, q: "_", want: []string{"id"}},
		{name: "no q", doc: Document{ID: "a", OriginalText: "a"}, q: "", want: []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := documentSearchMatches(test.doc, test.q)
			if len(got) != len(test.want) {
				t.Fatalf("matched = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("matched = %v, want %v", got, test.want)
				}
			}
		})
	}
}

// A q search counts and serves at most DocumentSearchCap matches.
func TestListDocumentsCapsQSearch(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	for i := 0; i < DocumentSearchCap+5; i++ {
		id := "match-" + itoa(i)
		if _, err := store.PutDocument(ctx, "b1", id, Document{OriginalText: "shared text"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.PutDocument(ctx, "b1", "other", Document{OriginalText: "nothing here"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListDocuments(ctx, DocumentQuery{BankID: "b1", Query: "match", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != DocumentSearchCap || !page.TotalIsEstimate {
		t.Fatalf("total = %d estimate = %v, want %d true", page.Total, page.TotalIsEstimate, DocumentSearchCap)
	}
	if len(page.Documents) != 10 {
		t.Fatalf("page = %d, want 10", len(page.Documents))
	}
	// The pages stop where the count does, so an offset at the cap serves nothing.
	page, err = store.ListDocuments(ctx, DocumentQuery{BankID: "b1", Query: "match", Limit: 10, Offset: DocumentSearchCap})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Documents) != 0 {
		t.Fatalf("page past the cap = %d, want 0", len(page.Documents))
	}
	// Without q nothing is capped or estimated.
	page, err = store.ListDocuments(ctx, DocumentQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != DocumentSearchCap+6 || page.TotalIsEstimate {
		t.Fatalf("unfiltered total = %d estimate = %v", page.Total, page.TotalIsEstimate)
	}
}

func rawScopes(raw string) json.RawMessage {
	if raw == "" {
		return nil
	}
	return json.RawMessage(raw)
}

func tagsOf(raw string) []string {
	var tags []string
	if raw == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(raw), &tags)
	return tags
}

func scopeKeys(scopes map[string]bool) []string {
	out := make([]string, 0, len(scopes))
	for key := range scopes {
		out = append(out, key)
	}
	return out
}

// A retag's swept observation is rebuilt from every source that stays: the
// surviving co-sources — including one belonging to another document — are
// requeued with the moved units, so the next pass folds them again
// (documents.py:578-588).
func TestRetagDocumentMemoriesRequeuesSurvivingCoSources(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	for _, unit := range []MemoryUnit{
		{ID: "moved", BankID: "b1", Text: "a fact", DocumentID: "doc-1", FactType: TypeWorld, Tags: []string{"session:a"}},
		{ID: "cosource", BankID: "b1", Text: "another fact", DocumentID: "doc-2", FactType: TypeWorld, Tags: []string{"session:b"}},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	observationID, err := store.CreateObservation(ctx, "b1", "Alice holds both facts", []string{"moved", "cosource"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkConsolidated(ctx, "b1", []string{"moved", "cosource"}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.RetagDocumentMemories(ctx, "b1", "doc-1", DocumentScopeChange{
		OldTags: []string{"session:a"}, NewScopes: []byte(`"shared"`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMemory(ctx, "b1", observationID); err == nil {
		t.Fatal("the swept observation is still stored")
	}
	moved, err := store.GetMemory(ctx, "b1", "moved")
	if err != nil {
		t.Fatal(err)
	}
	if moved.ConsolidatedAt != nil {
		t.Fatalf("moved unit consolidated_at = %v, want requeued", moved.ConsolidatedAt)
	}
	cosource, err := store.GetMemory(ctx, "b1", "cosource")
	if err != nil {
		t.Fatal(err)
	}
	if cosource.ConsolidatedAt != nil {
		t.Fatalf("surviving co-source consolidated_at = %v, want requeued", cosource.ConsolidatedAt)
	}
}
