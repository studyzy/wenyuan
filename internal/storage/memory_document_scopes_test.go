package storage

import (
	"context"
	"encoding/json"
	"testing"
)

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
