package reporting

import (
	"encoding/json"
	"testing"
)

// decodeValidated parses a raw preview body and runs it through the validator,
// returning the request and the flat list of structured items.
func decodeValidated(t *testing.T, body string) (consolidationScopesPreviewRequest, []string) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("bad test body %q: %v", body, err)
	}
	req, errs := decodeConsolidationPreview(raw)
	types := make([]string, 0, len(errs))
	for _, e := range errs {
		types = append(types, e.Type+" "+joinLoc(e.Loc))
	}
	return req, types
}

func joinLoc(loc []string) string {
	out := ""
	for i, segment := range loc {
		if i > 0 {
			out += "."
		}
		out += segment
	}
	return out
}

// The preview request is validated as strictly as the reference's model: a
// required strategies list, a bounded sample_limit, and a tags_match drawn from
// the accepted set — each with the framework's structured item.
func TestDecodeConsolidationPreviewValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"missing strategies", `{}`, []string{"missing body.strategies"}},
		{"null strategies", `{"strategies":null}`, []string{"list_type body.strategies"}},
		{"strategies wrong type", `{"strategies":"x"}`, []string{"list_type body.strategies"}},
		{"sample_limit too large", `{"strategies":[],"sample_limit":100}`, []string{"less_than_equal body.sample_limit"}},
		{"sample_limit negative", `{"strategies":[],"sample_limit":-1}`, []string{"greater_than_equal body.sample_limit"}},
		{"sample_limit null", `{"strategies":[],"sample_limit":null}`, []string{"int_type body.sample_limit"}},
		{"sample_limit string", `{"strategies":[],"sample_limit":"x"}`, []string{"int_parsing body.sample_limit"}},
		{
			"unknown tags_match",
			`{"strategies":[{"scopes":[{"tags":["a"],"tags_match":"any"}]}]}`,
			[]string{"value_error body.strategies.0.scopes.0.tags_match"},
		},
		{"valid", `{"strategies":[]}`, nil},
		{"valid defaults", `{"strategies":[{"scopes":[{"tags":["a"]}]}]}`, nil},
		{"whitespace-only tag is usable", `{"strategies":[{"scopes":[{"tags":[" "]}],"observations_mission":"m"}]}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, got := decodeValidated(t, tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("items = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("item %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
			if tc.want == nil && req.SampleLimit != 5 {
				t.Fatalf("sample_limit default = %d, want 5", req.SampleLimit)
			}
		})
	}
}

// A boolean and a numeric string coerce to the framework's int, exactly as
// pydantic's lax mode does; a fractional number does not.
func TestDecodeConsolidationPreviewCoercesInts(t *testing.T) {
	req, errs := decodeValidated(t, `{"strategies":[{"scopes":[{"tags":["a"]}],"max_observations_per_scope":true}]}`)
	if len(errs) != 0 {
		t.Fatalf("true must coerce, got %v", errs)
	}
	if len(req.Strategies) != 1 || req.Strategies[0].MaxObservationsPerScope == nil ||
		*req.Strategies[0].MaxObservationsPerScope != 1 {
		t.Fatalf("bool setting = %+v, want 1", req.Strategies)
	}

	if _, errs := decodeValidated(t, `{"strategies":[],"sample_limit":"3"}`); len(errs) != 0 {
		t.Fatalf("numeric string must coerce, got %v", errs)
	}

	_, errs = decodeValidated(t, `{"strategies":[],"sample_limit":3.5}`)
	if len(errs) != 1 || errs[0] != "int_from_float body.sample_limit" {
		t.Fatalf("fractional sample_limit = %v, want int_from_float", errs)
	}
}

// A whitespace-only tag is a non-empty string for the reference, so a strategy
// that sets a mission alongside it is active.
func TestWhitespaceOnlyTagKeepsStrategyActive(t *testing.T) {
	if !(consolidationScopePattern{Tags: []string{" "}}).patternValid() {
		t.Fatal("a whitespace-only tag must be a usable pattern")
	}
	if (consolidationScopePattern{Tags: []string{""}}).patternValid() {
		t.Fatal("an empty tag must not be a usable pattern")
	}
	mission := "m"
	active := strategyActive(consolidationStrategySpec{
		Scopes: []consolidationScopePattern{{Tags: []string{" "}}}, Mission: &mission,
	})
	if !active {
		t.Fatal("a strategy with a usable rule and a setting must be active")
	}
}
