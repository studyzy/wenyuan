package bankapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The process layer of a bank's configuration is the environment a reference
// deployment exported: a key the operator set must reach the value a bank reads,
// and a key they did not set must stay at the reference's shipped default.
func TestPublicConfigDefaultsResolveProcessKeys(t *testing.T) {
	values := map[string]string{
		"WENYUAN_API_RETAIN_CHUNK_SIZE":                     "1500",
		"WENYUAN_API_OBSERVATIONS_MISSION":                  "Track decisions",
		"WENYUAN_API_ENABLE_GRAPH_RETRIEVAL":                "false",
		"WENYUAN_API_CONSOLIDATION_LLM_BATCH_SIZE":          "16",
		"WENYUAN_API_MAX_OBSERVATIONS_PER_SCOPE":            "7",
		"WENYUAN_API_REFLECT_MISSION":                       "Be precise",
		"WENYUAN_API_CONSOLIDATION_SOURCE_FACTS_MAX_TOKENS": "2048",
	}
	cfg := config.FromEnv(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	got := PublicConfigDefaults(cfg)

	for key, want := range map[string]any{
		"retain_chunk_size":                     1500,
		"observations_mission":                  "Track decisions",
		"enable_graph_retrieval":                false,
		"consolidation_llm_batch_size":          16,
		"max_observations_per_scope":            7,
		"reflect_mission":                       "Be precise",
		"consolidation_source_facts_max_tokens": 2048,
	} {
		if got[key] != want {
			t.Errorf("%s = %#v, want %#v", key, got[key], want)
		}
	}
}

// A key the deployment did not set keeps the reference's default, and the
// pointer-typed fields stay null rather than becoming empty strings.
func TestPublicConfigDefaultsKeepReferenceDefaults(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	got := PublicConfigDefaults(cfg)

	for key, want := range map[string]any{
		"retain_chunk_size":                         3000,
		"retain_structured_chunk_size":              nil,
		"retain_mission":                            nil,
		"enable_text_search":                        true,
		"enable_graph_retrieval":                    true,
		"consolidation_llm_batch_size":              8,
		"consolidation_llm_parallelism":             4,
		"max_observations_per_scope":                -1,
		"recall_max_tokens":                         2048,
		"recall_chunks_max_tokens":                  1000,
		"recall_budget_fixed_mid":                   300,
		"reflect_source_facts_max_tokens":           -1,
		"disposition_empathy":                       nil,
		"observations_mission":                      nil,
		"mental_model_min_refresh_interval_seconds": 0,
	} {
		if got[key] != want {
			t.Errorf("%s = %#v, want the reference default %#v", key, got[key], want)
		}
	}
}

// The configuration read serves the process value for a bank that overrides
// nothing, which is what makes a deployment's environment visible to a client.
func TestConfigReadUsesProcessDefaults(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	cfg := config.FromEnv(func(key string) (string, bool) {
		if key == "WENYUAN_API_RETAIN_CHUNK_SIZE" {
			return "1500", true
		}
		return "", false
	})
	h := NewHandlers(store).WithConfigAPI(true).WithConfigDefaults(PublicConfigDefaults(cfg))
	mux := newMux(h)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks/b1/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Config["retain_chunk_size"] != float64(1500) {
		t.Fatalf("retain_chunk_size = %#v, want the process value 1500", body.Config["retain_chunk_size"])
	}
}

// The list row reports the bank's resolved disposition and mission, which is
// what the reference overlays on the stored columns: a bank whose configuration
// names a trait reports it.
func TestListRowReportsResolvedDispositionAndMission(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	cfg := config.FromEnv(func(key string) (string, bool) {
		switch key {
		case "WENYUAN_API_DISPOSITION_SKEPTICISM":
			return "5", true
		case "WENYUAN_API_REFLECT_MISSION":
			return "Be careful", true
		}
		return "", false
	})
	h := NewHandlers(store).WithConfigAPI(true).WithConfigDefaults(PublicConfigDefaults(cfg))
	mux := newMux(h)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Banks []struct {
			Disposition struct {
				Skepticism int `json:"skepticism"`
			} `json:"disposition"`
			Mission string `json:"mission"`
		} `json:"banks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Banks) != 1 {
		t.Fatalf("banks = %+v", body.Banks)
	}
	if body.Banks[0].Disposition.Skepticism != 5 {
		t.Errorf("skepticism = %d, want the configured 5", body.Banks[0].Disposition.Skepticism)
	}
	if body.Banks[0].Mission != "Be careful" {
		t.Errorf("mission = %q, want the configured mission", body.Banks[0].Mission)
	}
}

// A bank whose own configuration names a trait wins over the process default,
// and a bank that names nothing keeps its stored column.
func TestListRowPrefersTheBanksOwnConfig(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One",
		Disposition: map[string]int{storage.DispositionKeySkepticism: 2}})
	if _, err := store.UpdateBank(ctx, "b1", storage.BankPatch{}); err != nil {
		t.Fatal(err)
	}
	cfg := config.FromEnv(func(key string) (string, bool) {
		if key == "WENYUAN_API_DISPOSITION_SKEPTICISM" {
			return "5", true
		}
		return "", false
	})
	h := NewHandlers(store).WithConfigAPI(true).WithConfigDefaults(PublicConfigDefaults(cfg))
	mux := newMux(h)

	// The bank overrides the trait through its own configuration.
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
		`{"updates":{"disposition_skepticism":1}}`); rec.Code != http.StatusOK {
		t.Fatalf("config write status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks", "")
	var body struct {
		Banks []struct {
			Disposition struct {
				Skepticism int `json:"skepticism"`
			} `json:"disposition"`
		} `json:"banks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Banks[0].Disposition.Skepticism != 1 {
		t.Errorf("skepticism = %d, want the bank's own 1", body.Banks[0].Disposition.Skepticism)
	}
}

// The profile a create/replace/partial-update returns reports the bank's
// configuration overlaid on the legacy columns, exactly as the reference's
// update_bank returns its profile read-back: a bank whose configuration names a
// trait or a mission must report them, not the columns it was created with.
func TestProfileResponseOverlaysConfigDispositionAndMission(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	h := NewHandlers(store).WithConfigAPI(true).
		WithConfigDefaults(PublicConfigDefaults(config.FromEnv(func(string) (string, bool) { return "", false })))
	mux := newMux(h)

	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
		`{"updates":{"disposition_skepticism":5,"reflect_mission":"be careful"}}`); rec.Code != http.StatusOK {
		t.Fatalf("config write status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1", `{"name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("partial update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var profile BankResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Disposition.Skepticism != 5 {
		t.Errorf("skepticism = %d, want the configured 5", profile.Disposition.Skepticism)
	}
	if profile.Mission != "be careful" {
		t.Errorf("mission = %q, want the configured mission", profile.Mission)
	}
	if profile.Background != profile.Mission {
		t.Errorf("background = %q, want it to echo the mission", profile.Background)
	}
}

// The deployment's JSON-valued settings reach the configuration read as the
// parsed container, not the raw environment string, which is what the reference
// stores after json.loads.
func TestPublicConfigDefaultsParseJSONProcessKeys(t *testing.T) {
	values := map[string]string{
		"WENYUAN_API_MCP_ENABLED_TOOLS":              "recall, retain ,",
		"WENYUAN_API_OBSERVATION_SCOPE_LIMITS":       `[{"scope":"shared","limit":5}]`,
		"WENYUAN_API_CONSOLIDATION_STRATEGIES":       `[{"name":"s1"}]`,
		"WENYUAN_API_KNOWLEDGE_PAGE_DEFAULT_TRIGGER": `{"min_facts":3}`,
		"WENYUAN_API_LLM_GEMINI_SAFETY_SETTINGS":     `[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"BLOCK_NONE"}]`,
	}
	cfg := config.FromEnv(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	got := PublicConfigDefaults(cfg)

	tools, ok := got["mcp_enabled_tools"].([]string)
	if !ok || len(tools) != 2 || tools[0] != "recall" || tools[1] != "retain" {
		t.Fatalf("mcp_enabled_tools = %#v, want the split list", got["mcp_enabled_tools"])
	}
	scopes, ok := got["observation_scope_limits"].([]any)
	if !ok || len(scopes) != 1 {
		t.Fatalf("observation_scope_limits = %#v, want the parsed list", got["observation_scope_limits"])
	}
	strategies, ok := got["consolidation_strategies"].([]any)
	if !ok || len(strategies) != 1 {
		t.Fatalf("consolidation_strategies = %#v, want the parsed list", got["consolidation_strategies"])
	}
	trigger, ok := got["knowledge_page_default_trigger"].(map[string]any)
	if !ok || trigger["min_facts"] != float64(3) {
		t.Fatalf("knowledge_page_default_trigger = %#v, want the parsed object", got["knowledge_page_default_trigger"])
	}
	safety, ok := got["llm_gemini_safety_settings"].([]any)
	if !ok || len(safety) != 1 {
		t.Fatalf("llm_gemini_safety_settings = %#v, want the parsed list", got["llm_gemini_safety_settings"])
	}
}

// A key the deployment did not set keeps the reference's null for these
// JSON-valued and list-valued fields rather than an empty container or the
// empty string.
func TestPublicConfigDefaultsKeepJSONKeysNull(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	got := PublicConfigDefaults(cfg)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{
		"mcp_enabled_tools",
		"observation_scope_limits",
		"consolidation_strategies",
		"knowledge_page_default_trigger",
		"llm_gemini_safety_settings",
	} {
		raw, present := wire[key]
		if !present || string(raw) != "null" {
			t.Fatalf("%s = %s, want null (present=%v)", key, raw, present)
		}
	}
}
