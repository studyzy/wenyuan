package config

import (
	"strings"
	"testing"
)

// The default_observation_scopes environment value is validated at config
// construction: an invalid mode, malformed JSON or a scope list whose members
// are not tag lists refuses start-up naming the key, the way the reference's
// _parse_observation_scopes_env raises at config construction
// (config.py:361-393). The shapes retain accepts are honoured.
func TestDefaultObservationScopesEnvStrictlyParsed(t *testing.T) {
	cases := []struct {
		raw     string
		refused bool
	}{
		{"", false},
		{"   ", false},
		{"combined", false},
		{" shared ", false},
		{"per_tag", false},
		{`[["topic:*"]]`, false},
		{`[[]]`, false},
		{`[]`, false},
		{"everything", true},
		// A JSON-quoted mode name is not decoded: the parser only reads JSON
		// when the stripped value starts with '[', so this is an unknown mode.
		{`"combined"`, true},
		{"[", true},
		{`["topic:a"]`, true},
		{`[["a", 1]]`, true},
		{`[1, 2]`, true},
		{`{}`, true},
		{"1", true},
	}
	for _, tc := range cases {
		cfg := FromEnv(envMap(map[string]string{EnvDefaultObservationScopes: tc.raw}))
		err := cfg.ValidateSupported()
		if tc.refused {
			if err == nil {
				t.Fatalf("raw %q: resolved without refusal, want start-up refusal", tc.raw)
			}
			if !strings.Contains(err.Error(), EnvDefaultObservationScopes) {
				t.Fatalf("raw %q: refusal %v does not name the key", tc.raw, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("raw %q: refused: %v", tc.raw, err)
		}
	}
}

// A blank value is "not configured", not a refused one, and an accepted scope
// list still resolves to its decoded value.
func TestDefaultObservationScopesEnvStillResolvesAcceptedValues(t *testing.T) {
	cfg := FromEnv(envMap(map[string]string{EnvDefaultObservationScopes: `[["topic:*"]]`}))
	scopes, ok := cfg.ProcessDefaults.DefaultObservationScopes.([]any)
	if !ok || len(scopes) != 1 {
		t.Fatalf("resolved %#v, want one scope list", cfg.ProcessDefaults.DefaultObservationScopes)
	}
	if cfg.ValidateSupported() != nil {
		t.Fatal("an accepted value must not be refused")
	}
}
