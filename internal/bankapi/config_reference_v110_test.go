package bankapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference release that made recall's prefer_observations nullable also
// added its process default and retain's bank-level observation scoping, so both
// are bank-configurable keys. The read reports them resolved (the process
// defaults under the bank's overrides), and the write accepts exactly the shapes
// the reference's own validator accepts.
func TestBankConfigCarriesTheObservationDefaults(t *testing.T) {
	mux, _ := configServer(t)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks/b1/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	body := decodeConfig(t, rec.Body.Bytes())
	resolved := body.Config
	// The process default for the scoping is null, which the reference resolves
	// as "combined"; the preference default is off.
	if value, present := resolved["recall_prefer_observations"]; !present || value != false {
		t.Fatalf("recall_prefer_observations = %v (present %v), want false", value, present)
	}
	if value, present := resolved["default_observation_scopes"]; !present || value != nil {
		t.Fatalf("default_observation_scopes = %v (present %v), want null", value, present)
	}

	// A mode name and a glob scope list are both accepted and stored.
	for _, updates := range []string{
		`{"updates":{"recall_prefer_observations":true}}`,
		`{"updates":{"default_observation_scopes":"shared"}}`,
		`{"updates":{"default_observation_scopes":[["topic:*"],["user:*"]]}}`,
		`{"updates":{"default_observation_scopes":[[]]}}`,
	} {
		if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", updates); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %s", updates, rec.Code, rec.Body.String())
		}
	}

	// Values retain would not accept are refused with the reference's message.
	cases := []struct {
		name   string
		body   string
		detail string
	}{
		{"unknown mode", `{"updates":{"default_observation_scopes":"everything"}}`,
			`default_observation_scopes must be one of combined, per_tag, all_combinations, shared or a list of tag lists (e.g. [["topic:*"]]), got 'everything'`},
		{"flat tag list", `{"updates":{"default_observation_scopes":["topic:a"]}}`,
			`default_observation_scopes must be one of combined, per_tag, all_combinations, shared or a list of tag lists (e.g. [["topic:*"]]), got ['topic:a']`},
		{"non-string tag", `{"updates":{"default_observation_scopes":[["a",1]]}}`,
			`default_observation_scopes must be one of combined, per_tag, all_combinations, shared or a list of tag lists (e.g. [["topic:*"]]), got [['a', 1]]`},
		{"whole number", `{"updates":{"default_observation_scopes":1}}`,
			"default_observation_scopes must be a string or a list, got int"},
	}
	for _, tc := range cases {
		rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		var refusal struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
			t.Fatal(err)
		}
		if refusal.Detail != tc.detail {
			t.Fatalf("%s: detail = %q, want %q", tc.name, refusal.Detail, tc.detail)
		}
	}
}

// The process layer parses the scoping value the way the reference's env parser
// does: a mode name as written, a JSON scope list as decoded, and anything unset
// or unparseable as null. The reference validates the value at config
// construction, so an invalid one is additionally refused at start-up
// (config.py:_parse_observation_scopes_env) rather than resolving silently.
func TestProcessDefaultsParseTheObservationScopes(t *testing.T) {
	cases := []struct {
		raw  string
		want any
	}{
		{"", nil},
		{"   ", nil},
		{"shared", "shared"},
		{"[", nil},
		{`[["topic:*"]]`, nil}, // decoded, compared below
	}
	for _, tc := range cases {
		cfg := config.FromEnv(func(key string) (string, bool) {
			if key == config.EnvDefaultObservationScopes {
				return tc.raw, true
			}
			return "", false
		})
		got := cfg.ProcessDefaults.DefaultObservationScopes
		if tc.raw == `[["topic:*"]]` {
			scopes, ok := got.([]any)
			if !ok || len(scopes) != 1 {
				t.Fatalf("raw %q: parsed %#v, want one scope list", tc.raw, got)
			}
			scope, _ := scopes[0].([]any)
			if len(scope) != 1 || scope[0] != "topic:*" {
				t.Fatalf("raw %q: parsed %#v, want [[\"topic:*\"]]", tc.raw, scopes)
			}
		} else if got != tc.want {
			t.Fatalf("raw %q: parsed %#v, want %#v", tc.raw, got, tc.want)
		}
		// The values the reference's parser accepts resolve without a refusal;
		// the ones it raises on refuse start-up naming the key.
		err := cfg.ValidateSupported()
		if tc.raw == "" || tc.raw == "   " || tc.raw == "shared" || tc.raw == `[["topic:*"]]` {
			if err != nil {
				t.Fatalf("raw %q: refused: %v", tc.raw, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("raw %q: resolved without the start-up refusal", tc.raw)
		}
		if !strings.Contains(err.Error(), config.EnvDefaultObservationScopes) {
			t.Fatalf("raw %q: refusal %v does not name the key", tc.raw, err)
		}
	}
	// The preference default is off unless configured, and honoured when it is.
	unset := config.FromEnv(func(string) (string, bool) { return "", false })
	if unset.ProcessDefaults.RecallPreferObservations {
		t.Fatal("recall_prefer_observations defaulted on")
	}
	on := config.FromEnv(func(key string) (string, bool) {
		if key == config.EnvRecallPreferObservations {
			return "true", true
		}
		return "", false
	})
	if !on.ProcessDefaults.RecallPreferObservations {
		t.Fatal("recall_prefer_observations ignored the configured value")
	}
}

// The values land in the bank's config, not just in the response.
func TestBankConfigStoresTheObservationDefaults(t *testing.T) {
	mux, store := configServer(t)
	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
		`{"updates":{"recall_prefer_observations":true,"default_observation_scopes":[["topic:*"]]}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	bank, err := store.GetBank(t.Context(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if bank.Config["recall_prefer_observations"] != true {
		t.Fatalf("stored %#v", bank.Config["recall_prefer_observations"])
	}
	scopes, ok := bank.Config["default_observation_scopes"].([]any)
	if !ok || len(scopes) != 1 {
		t.Fatalf("stored %#v, want [[\"topic:*\"]]", bank.Config["default_observation_scopes"])
	}
	if scopes[0].([]any)[0] != "topic:*" {
		t.Fatalf("stored %#v", scopes)
	}
	var _ storage.Bank
}
