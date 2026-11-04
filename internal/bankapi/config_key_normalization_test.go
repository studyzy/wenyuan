package bankapi

import "testing"

// TestNormalizeConfigKeyAcceptsTheReferenceSpelling pins the wire contract the
// reference defines for config keys: its clients may spell a key either in
// field form ("llm_provider") or in its own env form ("HINDSIGHT_API_LLM_PROVIDER"),
// and normalize_config_key strips only that prefix before lowercasing
// (hindsight_api/config.py:115-132). This service renamed its own environment to
// WENYUAN_API_* — a rename that stops at the wire — so the same request must be
// accepted with either spelled prefix. Reverting either arm of normalizeConfigKey
// makes this test FAIL: dropping the HINDSIGHT arm re-introduces the rejection
// of the reference's spelling ("hindsight_api_llm_provider" is not a
// configurable field), dropping the WENYUAN arm breaks every client carrying
// this service's spelling.
func TestNormalizeConfigKeyAcceptsTheReferenceSpelling(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "field form", key: "llm_provider", want: "llm_provider"},
		{name: "this service's env form", key: "WENYUAN_API_LLM_PROVIDER", want: "llm_provider"},
		{name: "the reference's env form", key: "HINDSIGHT_API_LLM_PROVIDER", want: "llm_provider"},
		{name: "reference spelling is case preserved then lowered", key: "HINDSIGHT_API_Memory_Defense", want: "memory_defense"},
		{name: "unknown prefix is left alone", key: "OTHER_API_LLM_PROVIDER", want: "other_api_llm_provider"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeConfigKey(tt.key); got != tt.want {
				t.Fatalf("normalizeConfigKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// TestUpdateBankConfigAcceptsTheReferencePrefixedKey drives the normalization
// through the served update path: a PATCH whose updates carry the reference's
// HINDSIGHT_API_ spelling must normalize to the same field a WENYUAN_API_-spelled
// key normalizes to, not surface as an unknown field. Reverting the HINDSIGHT
// arm of normalizeConfigKey makes this test FAIL with the unknown-field refusal.
func TestUpdateBankConfigAcceptsTheReferencePrefixedKey(t *testing.T) {
	got := normalizeConfigKey("HINDSIGHT_API_ENABLE_OBSERVATIONS")
	if got != "enable_observations" {
		t.Fatalf("reference-spelled key normalized to %q, want enable_observations", got)
	}
}
