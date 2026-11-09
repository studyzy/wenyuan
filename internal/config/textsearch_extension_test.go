package config

import "testing"

// The text-search extension key is refused for every backend this build does
// not implement, and accepted for the two it does.
//
// The table-driven contract test probes a fixed sample of non-default values,
// and "pg_search" is not among them, so it would pass whether or not this build
// accepted pg_search. That matters more than usual here: pg_search is the only
// backend that tokenizes Chinese, so a regression in this predicate turns
// every Chinese deployment back into one that can match a sentence only in
// full.
func TestTextSearchExtensionAcceptsImplementedBackends(t *testing.T) {
	accepted := []string{"native", "pg_search", "PG_Search", " pg_search "}
	for _, value := range accepted {
		cfg := FromEnv(envLookup(map[string]string{EnvTextSearchExtension: value}))
		if err := cfg.ValidateSupported(); err != nil {
			t.Fatalf("%s=%q was refused: %v", EnvTextSearchExtension, value, err)
		}
	}

	// The reference's other backends stay refused: this build has no
	// implementation behind them, and accepting one would serve the native arm
	// while reporting the configured backend.
	for _, value := range []string{"vchord", "pgroonga", "pg_textsearch", "postgres"} {
		cfg := FromEnv(envLookup(map[string]string{EnvTextSearchExtension: value}))
		if err := cfg.ValidateSupported(); err == nil {
			t.Fatalf("%s=%q was accepted, want it refused as unimplemented", EnvTextSearchExtension, value)
		}
	}

	// The tokenizer reaches the resolved configuration trimmed, which is what the
	// storage layer's normalizer lowercases and validates before it embeds the
	// value in SQL.
	cfg := FromEnv(envLookup(map[string]string{
		EnvTextSearchExtension:   "pg_search",
		EnvTextSearchPGTokenizer: "  Jieba ",
	}))
	if cfg.TextSearchExtension != "pg_search" {
		t.Fatalf("text search extension = %q", cfg.TextSearchExtension)
	}
	if cfg.DBTuning.PGTokenizer != "Jieba" {
		t.Fatalf("tokenizer = %q, want the configured value trimmed", cfg.DBTuning.PGTokenizer)
	}
}
