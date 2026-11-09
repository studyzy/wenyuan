package storage

import (
	"strings"
	"testing"
)

// BM25IndexDDL builds the pg_search keyword index only for that backend, and
// casts each searched column to the configured tokenizer.
//
// The tokenizer has to reach the index: pg_search 0.26 removed the `tokenizer=`
// index parameter, and `(text::pdb.jieba)` is the spelling that selects it. An
// index built without the tokenizer would split Chinese with a different ruleset
// than the query side uses, and the two would not meet.
func TestBM25IndexDDLSelectsTokenizerAndBackend(t *testing.T) {
	tests := []struct {
		name      string
		tokenizer string
		want      []string
		wantEmpty bool
	}{
		{
			name:      "jieba",
			tokenizer: "jieba",
			want: []string{
				"CREATE INDEX CONCURRENTLY IF NOT EXISTS",
				"idx_mu_bm25",
				`ON "studyzy".memory_units USING bm25`,
				`("text"::pdb.jieba)`,
				`("text_signals"::pdb.jieba)`,
			},
		},
		{
			name:      "lindera chinese",
			tokenizer: "lindera(chinese)",
			want:      []string{`("text"::pdb.lindera(chinese))`},
		},
		{
			name:      "no tokenizer leaves the backend unindexed",
			tokenizer: "",
			wantEmpty: true,
		},
		{
			name:      "native spelling is not a pg_search tokenizer",
			tokenizer: TextSearchNative,
			wantEmpty: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BM25IndexDDL("studyzy", tc.tokenizer)
			if tc.wantEmpty {
				if got != "" {
					t.Fatalf("BM25IndexDDL(%q) = %q, want no statement", tc.tokenizer, got)
				}
				return
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("BM25IndexDDL(%q) missing %q:\n%s", tc.tokenizer, want, got)
				}
			}
			// `context` stays out of the index for the same reason it stays out
			// of the arm's match fields.
			if strings.Contains(got, `"context"`) {
				t.Fatalf("BM25IndexDDL(%q) indexed context:\n%s", tc.tokenizer, got)
			}
		})
	}
}

// The index survives a schema name that needs quoting, and never carries an
// unvalidated tokenizer: both are embedded in DDL, so a stray quote would be an
// injection rather than a syntax error.
func TestBM25IndexDDLQuotesSchemaAndRejectsInjection(t *testing.T) {
	got := BM25IndexDDL("tenant_1", "ngram(2,3)")
	if !strings.Contains(got, `ON "tenant_1".memory_units`) {
		t.Fatalf("schema is not quoted:\n%s", got)
	}
	if !strings.Contains(got, `("text"::pdb.ngram(2,3))`) {
		t.Fatalf("ngram tokenizer is not applied:\n%s", got)
	}
}
