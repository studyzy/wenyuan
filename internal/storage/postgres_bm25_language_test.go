package storage

import (
	"strings"
	"testing"
)

// The native text-search dictionary is one deployment setting that is embedded
// in three places — the expression every write stores into
// memory_units.search_vector, the keyword arm's to_tsquery, and the lexeme
// mapping the term-selection statistics query reads. All three have to carry
// the configured dictionary, and all three have to fall back to the reference's
// own default when the deployment names none, because a row tokenized under one
// dictionary is not equivalent under another.
func TestNativeDictionaryReachesEveryKeywordSite(t *testing.T) {
	q := RecallQuery{BankID: "bank", Types: []string{string(TypeWorld)}}

	defaultStore := &PostgresStore{schema: "public", textSearchExtension: TextSearchNative, tuning: DefaultTuning()}
	configured := &PostgresStore{
		schema: "public", textSearchExtension: TextSearchNative, tsNativeLanguage: "simple", tuning: DefaultTuning(),
	}

	cases := []struct {
		name     string
		store    *PostgresStore
		want     string
		unwanted string
	}{
		{name: "default deployment stays english", store: defaultStore, want: "'english'", unwanted: "'simple'"},
		{name: "configured deployment uses its dictionary", store: configured, want: "'simple'", unwanted: "'english'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			armSQL, _ := tc.store.bm25ArmQuery(q, "alice | berlin", 20)
			if !strings.Contains(armSQL, "to_tsquery("+tc.want+", $3)") {
				t.Fatalf("keyword arm does not tokenize with %s:\n%s", tc.want, armSQL)
			}
			if strings.Contains(armSQL, "to_tsquery("+tc.unwanted) {
				t.Fatalf("keyword arm tokenizes with %s:\n%s", tc.unwanted, armSQL)
			}

			vector := tc.store.searchVectorExpr("$3", "$4", "$5")
			if !strings.Contains(vector, "to_tsvector("+tc.want+", ") {
				t.Fatalf("stored vector is not built with %s: %s", tc.want, vector)
			}
			if strings.Contains(vector, "to_tsvector("+tc.unwanted) {
				t.Fatalf("stored vector is built with %s: %s", tc.unwanted, vector)
			}

			stats := tc.store.bm25TermStatsQuery()
			if !strings.Contains(stats, "to_tsvector("+tc.want+", t.tok)") {
				t.Fatalf("term statistics are not read under %s:\n%s", tc.want, stats)
			}
			if strings.Contains(stats, "to_tsvector("+tc.unwanted) {
				t.Fatalf("term statistics are read under %s:\n%s", tc.unwanted, stats)
			}
		})
	}
}

// A store whose resolved backend carried no dictionary still tokenizes with the
// reference's default rather than the empty string, which is what a store built
// directly (a test, or a caller that only needs the schema) would otherwise
// embed.
func TestNativeDictionaryFallsBackToEnglish(t *testing.T) {
	store := &PostgresStore{schema: "public", textSearchExtension: TextSearchNative, tuning: DefaultTuning()}
	if !strings.Contains(store.searchVectorExpr("$3", "''", "''"), "to_tsvector('english', ") {
		t.Fatalf("empty dictionary did not fall back: %s", store.searchVectorExpr("$3", "''", "''"))
	}
	if !strings.Contains(store.bm25TermStatsQuery(), "to_tsvector('english', t.tok)") {
		t.Fatalf("empty dictionary did not fall back:\n%s", store.bm25TermStatsQuery())
	}
}
