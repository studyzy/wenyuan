package bankapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// A strategy's own default_observation_scopes is stamped onto its items at
// retain time, so the reference runs the same shape check over it as over the
// top-level field, naming the strategy it sits under
// (config_resolver.py:604-612).
func TestRetainStrategyScopesShapeChecked(t *testing.T) {
	mux, _ := configServer(t)

	cases := []struct {
		name   string
		scopes string
		want   string
	}{
		{"unknown mode", `"bogus"`,
			"Invalid retain strategy 's': default_observation_scopes must be one of combined, per_tag, all_combinations, shared or a list of tag lists (e.g. [[\"topic:*\"]]), got 'bogus'"},
		{"flat tag list", `["topic:a"]`,
			"Invalid retain strategy 's': default_observation_scopes must be one of combined, per_tag, all_combinations, shared or a list of tag lists (e.g. [[\"topic:*\"]]), got ['topic:a']"},
		{"non-string tag", `[["a",1]]`,
			"Invalid retain strategy 's': default_observation_scopes must be one of combined, per_tag, all_combinations, shared or a list of tag lists (e.g. [[\"topic:*\"]]), got [['a', 1]]"},
		{"environment spelling", `"bogus"`,
			"Invalid retain strategy 's': default_observation_scopes must be one of combined, per_tag, all_combinations, shared or a list of tag lists (e.g. [[\"topic:*\"]]), got 'bogus'"},
	}
	for _, tc := range cases {
		key := "default_observation_scopes"
		if tc.name == "environment spelling" {
			key = "WENYUAN_API_DEFAULT_OBSERVATION_SCOPES"
		}
		body := `{"updates":{"retain_strategies":{"s":{"` + key + `":` + tc.scopes + `}}}}`
		rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		var refusal struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
			t.Fatal(err)
		}
		if refusal.Detail != tc.want {
			t.Fatalf("%s: detail = %q, want %q", tc.name, refusal.Detail, tc.want)
		}
	}
}

// The shapes retain accepts are stored, and null stays the "unset" tombstone.
func TestRetainStrategyScopesAcceptedShapes(t *testing.T) {
	mux, store := configServer(t)
	cases := []struct {
		raw  string
		want any
	}{
		{`"shared"`, "shared"},
		{`[["topic:*"]]`, []any{[]any{"topic:*"}}},
		{`null`, nil},
		{`[]`, []any{}},
	}
	for _, tc := range cases {
		body := `{"updates":{"retain_strategies":{"s":{"default_observation_scopes":` + tc.raw + `}}}}`
		if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", body); rec.Code != http.StatusOK {
			t.Fatalf("scopes %s: status = %d, body %s", tc.raw, rec.Code, rec.Body.String())
		}
		bank, err := store.GetBank(t.Context(), "b1")
		if err != nil {
			t.Fatal(err)
		}
		strategy, _ := bank.Config["retain_strategies"].(map[string]any)
		entry, _ := strategy["s"].(map[string]any)
		// A nested null is stored verbatim — the reference's dict merge keeps it
		// and every reader treats it as unset — so the assertion is on the value.
		if !reflect.DeepEqual(entry["default_observation_scopes"], tc.want) {
			t.Fatalf("scopes %s: stored %#v, want %#v", tc.raw, entry["default_observation_scopes"], tc.want)
		}
	}
}

// The type check of a strategy's other fields runs before the scopes check, as
// the reference validates the whole overrides dict's types first.
func TestRetainStrategyTypesReportedBeforeScopes(t *testing.T) {
	mux, _ := configServer(t)
	body := `{"updates":{"retain_strategies":{"s":{"retain_chunk_size":"big","default_observation_scopes":"bogus"}}}}`
	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	want := "Invalid retain strategy 's': retain_chunk_size must be an integer, got str"
	if got := rec.Body.String(); !strings.Contains(got, want) {
		t.Fatalf("body = %s, want it to contain %q", got, want)
	}
}
