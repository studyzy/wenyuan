package bankapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// decodeConfig reads a config response.
func decodeConfig(t *testing.T, body []byte) ConfigResponse {
	t.Helper()
	var out ConfigResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func configServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	h := NewHandlers(store)
	h.WithConfigAPI(true)
	// The composition root layers the process defaults under a bank's overrides,
	// so the resolved read exposes every configurable field — including the ones
	// a bank never sets. A handler set without them answers with only the
	// overrides, which is not what the route serves.
	h.WithConfigDefaults(PublicConfigDefaults(config.FromEnv(func(string) (string, bool) { return "", false })))
	return newMux(h), store
}

// The config read is the fully resolved configuration, not a fixed set of keys:
// every override the bank set appears in config (resolved) and in overrides
// (bank-only), and the resolver's disposition and mission columns surface too.
func TestBankConfigReadIsFullyResolved(t *testing.T) {
	mux, _ := configServer(t)

	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
		`{"updates":{"retain_chunk_size":120,"retain_mission":"keep decisions","enable_text_search":false}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("config write = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, mux, http.MethodGet, "/v1/default/banks/b1/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("config read = %d", rec.Code)
	}
	cfg := decodeConfig(t, rec.Body.Bytes())

	if value, _ := cfg.Overrides["retain_chunk_size"].(float64); value != 120 {
		t.Fatalf("overrides retain_chunk_size = %v, want 120", cfg.Overrides["retain_chunk_size"])
	}
	if cfg.Overrides["retain_mission"] != "keep decisions" {
		t.Fatalf("overrides retain_mission = %v", cfg.Overrides["retain_mission"])
	}
	if value, _ := cfg.Config["retain_chunk_size"].(float64); value != 120 {
		t.Fatalf("resolved config must carry the override: %v", cfg.Config["retain_chunk_size"])
	}
	if cfg.Config["enable_text_search"] != false {
		t.Fatalf("resolved config enable_text_search = %v, want false", cfg.Config["enable_text_search"])
	}
	// The disposition columns are part of the bank's overrides, so the resolved
	// config exposes them under the same flat keys the profile used.
	if _, ok := cfg.Config["disposition_skepticism"]; !ok {
		t.Fatalf("resolved config must carry the disposition keys: %v", cfg.Config)
	}
	if _, ok := cfg.Config["reflect_mission"]; !ok {
		t.Fatalf("resolved config must carry reflect_mission: %v", cfg.Config)
	}
}

// The resolved config layers the caller's tenant overrides over the bank's own,
// while overrides stays bank-only.
func TestBankConfigReadLayersTenantOverrides(t *testing.T) {
	mux, _ := configServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1/config", nil)
	req = httpx.WithTenantOverrides(req, map[string]any{"enable_reranking": false, "recall_max_tokens": 256})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("config read = %d", rec.Code)
	}
	cfg := decodeConfig(t, rec.Body.Bytes())

	if cfg.Config["enable_reranking"] != false {
		t.Fatalf("resolved config must layer the tenant override: %v", cfg.Config["enable_reranking"])
	}
	if _, ok := cfg.Overrides["enable_reranking"]; ok {
		t.Fatalf("overrides must stay bank-only: %v", cfg.Overrides)
	}
}

// An unknown or wrongly typed config key is the reference's 400, the accepted
// key set covers the reference's configurable fields, and a missing updates body
// is the reference's 422 required-field error.
func TestBankConfigWriteValidation(t *testing.T) {
	mux, store := configServer(t)

	// A body without updates is a required-field failure.
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing updates = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	// An unknown key (the reference's ValueError path) is a 400.
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", `{"updates":{"totally_unknown":1}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	// A wrongly typed known key is a 400, not a 422.
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", `{"updates":{"retain_chunk_size":"big"}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong type = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	// A non-positive segment size is a 400.
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", `{"updates":{"retain_chunk_size":0}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-positive chunk size = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	// Keys the reference allows but this port did not previously accept are now
	// stored, not refused.
	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
		`{"updates":{"store_document_text":false,"enable_observations":false,"recall_max_tokens":512}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("an accepted key = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	bank, err := store.GetBank(context.TODO(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if bank.Config["store_document_text"] != false || bank.Config["enable_observations"] != false {
		t.Fatalf("accepted keys must be stored: %v", bank.Config)
	}
}

// The config write distinguishes three rejection messages, as the reference
// does: a credential field, a server-level (static) field, and a truly unknown
// field — each naming every offending key, all at once.
func TestBankConfigWriteRejectionMessages(t *testing.T) {
	mux, _ := configServer(t)

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			"credential field",
			`{"updates":{"llm_api_key":"x"}}`,
			"Cannot set credential fields via API: ['llm_api_key']. Credentials (API keys, base URLs) must be set at server level only.",
		},
		{
			"static field",
			`{"updates":{"database_url":"x"}}`,
			"Cannot override static (server-level) fields: ['database_url'].",
		},
		{
			"several unknown keys",
			`{"updates":{"totally_unknown":1,"another_unknown":2}}`,
			"Unknown configuration fields: ['another_unknown', 'totally_unknown']. Valid configurable fields: ",
		},
	}
	for _, tc := range cases {
		rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s: body = %s, want it to contain %q", tc.name, rec.Body.String(), tc.want)
		}
	}
}

// The operational fields on the bank write body are applied through the config
// path rather than parsed and ignored.
func TestBankWriteAppliesOperationalConfig(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})

	patch := `{"retain_mission":"keep decisions","retain_extraction_mode":"custom",
		"retain_custom_instructions":"extract details","retain_chunk_size":120,
		"retain_structured_chunk_size":200,"retain_max_attachments_per_chunk":6,
		"enable_observations":false,"observations_mission":"stable facts",
		"enable_text_search":false,"enable_temporal_retrieval":false,
		"enable_graph_retrieval":false,"enable_reranking":false}`
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1", patch); rec.Code != http.StatusOK {
		t.Fatalf("patch bank = %d, body = %s", rec.Code, rec.Body.String())
	}
	bank, err := store.GetBank(context.TODO(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"retain_mission":                   "keep decisions",
		"retain_extraction_mode":           "custom",
		"retain_custom_instructions":       "extract details",
		"retain_chunk_size":                120,
		"retain_structured_chunk_size":     200,
		"retain_max_attachments_per_chunk": 6,
		"enable_observations":              false,
		"observations_mission":             "stable facts",
		"enable_text_search":               false,
		"enable_temporal_retrieval":        false,
		"enable_graph_retrieval":           false,
		"enable_reranking":                 false,
	}
	for key, expected := range want {
		if got := bank.Config[key]; got != expected {
			t.Fatalf("config[%q] = %#v, want %#v (config=%v)", key, got, expected, bank.Config)
		}
	}

	// The same fields on create reach the stored bank.
	create := `{"name":"Two","enable_reranking":false,"retain_chunk_size":64}`
	if rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/b2", create); rec.Code != http.StatusOK {
		t.Fatalf("put bank = %d, body = %s", rec.Code, rec.Body.String())
	}
	created, err := store.GetBank(context.TODO(), "b2")
	if err != nil {
		t.Fatal(err)
	}
	if created.Config["enable_reranking"] != false || created.Config["retain_chunk_size"] != 64 {
		t.Fatalf("created bank config = %v", created.Config)
	}
}

// The reflect mission and the disposition traits written through the bank
// routes are configuration overrides as well as bank-row values: the reference
// routes them through get_config_updates, so `GET /config` reports them in
// `overrides` and the resolved config carries them.
func TestBankWriteStoresMissionAndDispositionAsOverrides(t *testing.T) {
	mux, store := newTestServer(t)

	create := `{"name":"M","mission":"be terse",
		"disposition":{"skepticism":5,"literalism":2,"empathy":1}}`
	if rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/bm", create); rec.Code != http.StatusOK {
		t.Fatalf("put bank = %d, body = %s", rec.Code, rec.Body.String())
	}
	bank, err := store.GetBank(context.TODO(), "bm")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"reflect_mission":        "be terse",
		"disposition_skepticism": 5,
		"disposition_literalism": 2,
		"disposition_empathy":    1,
	}
	for key, expected := range want {
		if got := bank.Config[key]; got != expected {
			t.Fatalf("config[%q] = %#v, want %#v (config=%v)", key, got, expected, bank.Config)
		}
	}

	// The flat fields take precedence over the deprecated object, and the
	// partial update stores only what it carried.
	patch := `{"disposition":{"skepticism":1,"literalism":1,"empathy":1},"disposition_empathy":4}`
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/bm", patch); rec.Code != http.StatusOK {
		t.Fatalf("patch bank = %d, body = %s", rec.Code, rec.Body.String())
	}
	patched, err := store.GetBank(context.TODO(), "bm")
	if err != nil {
		t.Fatal(err)
	}
	if patched.Config["disposition_empathy"] != 4 {
		t.Fatalf("flat field must win: config = %v", patched.Config)
	}
	if patched.Config["disposition_skepticism"] != 1 {
		t.Fatalf("object member must be stored: config = %v", patched.Config)
	}
}

// The configuration read reports only the bank's stored overrides. The legacy
// disposition and mission columns are not consulted, so a bank whose columns
// carry values (the schema's neutral default, or a profile write) still reports
// an empty overrides map and a null disposition — which is what the reference's
// config endpoint answers, since it reads its config column and nothing else.
func TestBankConfigReadIgnoresLegacyDispositionColumns(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One",
		Disposition: map[string]int{storage.DispositionKeySkepticism: 4},
		Mission:     "legacy mission"})
	h := NewHandlers(store).WithConfigAPI(true).
		WithConfigDefaults(PublicConfigDefaults(config.FromEnv(func(string) (string, bool) { return "", false })))
	mux := newMux(h)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks/b1/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("config read = %d, body = %s", rec.Code, rec.Body.String())
	}
	cfg := decodeConfig(t, rec.Body.Bytes())
	if len(cfg.Overrides) != 0 {
		t.Fatalf("overrides must ignore the legacy columns: %v", cfg.Overrides)
	}
	for _, key := range []string{"disposition_skepticism", "reflect_mission"} {
		if value, present := cfg.Config[key]; !present || value != nil {
			t.Fatalf("config[%q] = %#v, want null (present=%v)", key, value, present)
		}
	}
}

// A bank whose stored configuration is invalid fails the read the way the
// reference's resolve_full_config does: a 500 naming the field, not a 200 with
// a configuration that could not retain.
func TestBankConfigReadRejectsInvalidResolvedConfig(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{BankID: "b1", Name: "One"})
	bad := map[string]any{"retain_chunk_size": 0}
	if _, err := store.UpdateBank(context.TODO(), "b1", storage.BankPatch{Config: &bad}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store).WithConfigAPI(true).
		WithConfigDefaults(PublicConfigDefaults(config.FromEnv(func(string) (string, bool) { return "", false })))
	mux := newMux(h)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks/b1/config", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("invalid stored config = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Detail != "retain_chunk_size must be >= 1, got 0" {
		t.Fatalf("detail = %q, want the reference's message", body.Detail)
	}
}
