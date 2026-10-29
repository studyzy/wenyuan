package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/banktemplate"
	"github.com/studyzy/wenyuan/internal/storage"
)

// v110TemplateBankFields are the v1.1.0 bank-config fields the template model
// declares and this export used to drop: the four decision-orchestration
// switches, the bank's default observation scoping and recall's
// prefer-observations default.
var v110TemplateBankFields = []string{
	"reflect_use_decision_model",
	"mental_model_refresh_use_decision_model",
	"mental_model_delta_use_decision_model",
	"consolidation_use_decision_model",
	"default_observation_scopes",
	"recall_prefer_observations",
}

// seededTemplateConfig is the stored configuration the export tests read.
func seededTemplateConfig() map[string]any {
	return map[string]any{
		"reflect_use_decision_model":              false,
		"mental_model_refresh_use_decision_model": false,
		"mental_model_delta_use_decision_model":   true,
		"consolidation_use_decision_model":        true,
		"default_observation_scopes":              []any{[]any{"topic:*"}},
		"recall_prefer_observations":              true,
	}
}

// The export carries every field the reference's template model declares,
// including the v1.1.0 additions: a bank storing them sees them in the
// manifest, and the trigger's full_refresh_cron rides with it.
func TestTemplateExportCarriesTheV110BankFields(t *testing.T) {
	h, store := newHandlers(t, true, true)
	config := seededTemplateConfig()
	if _, err := store.UpdateBank(context.Background(), "b1", storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutMentalModel(context.Background(), storage.MentalModel{
		ID: "goals", BankID: "b1", Subtype: storage.SubtypePinned, Name: "Goals",
		SourceQuery: "what are the goals?", Tags: []string{}, MaxTokens: 2048,
		Trigger: map[string]any{"full_refresh_cron": "0 3 * * 0"},
	}); err != nil {
		t.Fatal(err)
	}

	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export", "", h.ExportTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d, body = %s", rec.Code, rec.Body.String())
	}
	var manifest banktemplate.Manifest
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	for _, key := range v110TemplateBankFields {
		if _, present := manifest.Bank[key]; !present {
			t.Fatalf("exported bank section drops %q: %v", key, manifest.Bank)
		}
	}
	if manifest.Bank["recall_prefer_observations"] != true {
		t.Fatalf("exported recall_prefer_observations = %#v, want the stored true", manifest.Bank["recall_prefer_observations"])
	}
	scopes, _ := manifest.Bank["default_observation_scopes"].([]any)
	if len(scopes) != 1 {
		t.Fatalf("exported default_observation_scopes = %#v, want the stored scope list", manifest.Bank["default_observation_scopes"])
	}
	if len(manifest.MentalModels) != 1 {
		t.Fatalf("models = %+v", manifest.MentalModels)
	}
	if got := manifest.MentalModels[0].Trigger["full_refresh_cron"]; got != "0 3 * * 0" {
		t.Fatalf("exported full_refresh_cron = %#v, want the stored schedule", manifest.MentalModels[0].Trigger["full_refresh_cron"])
	}
}

// The exported manifest round-trips: an import stores the v1.1.0 fields
// verbatim in the target bank's config column, where the configuration read and
// the profile overlay consult them.
func TestTemplateRoundTripPreservesTheV110BankFields(t *testing.T) {
	h, store := newHandlers(t, true, true)
	config := seededTemplateConfig()
	if _, err := store.UpdateBank(context.Background(), "b1", storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export", "", h.ExportTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d, body = %s", rec.Code, rec.Body.String())
	}

	imported := serveJSON(h, http.MethodPost, "/v1/default/banks/b2/import", rec.Body.Bytes())
	if imported.Code != http.StatusOK {
		t.Fatalf("import: %d, body = %s", imported.Code, imported.Body.String())
	}
	bank, err := store.GetBank(context.Background(), "b2")
	if err != nil {
		t.Fatalf("the target bank must be created: %v", err)
	}
	want := seededTemplateConfig()
	for key, value := range want {
		stored, present := bank.Config[key]
		if !present {
			t.Fatalf("target config drops %q: %v", key, bank.Config)
		}
		if got, ok := value.([]any); ok {
			storedScopes, _ := stored.([]any)
			if len(storedScopes) != len(got) {
				t.Fatalf("stored %q = %#v, want %#v", key, stored, value)
			}
			continue
		}
		if stored != value {
			t.Fatalf("stored %q = %#v, want %#v", key, stored, value)
		}
	}
}

// A manifest whose bank section names a key the configuration endpoint refuses
// is rejected with the reference's own message, on the applying import and on
// the dry run alike: the reference persists the section through
// update_bank_config, whose validation refuses unknown fields.
func TestImportTemplateRejectsUnknownBankConfigKeys(t *testing.T) {
	h, store := newHandlers(t, true, true)
	body := `{"version":"1","bank":{"totally_unknown":1,"reflect_mission":"help"}}`
	for _, path := range []string{"/v1/default/banks/b1/import", "/v1/default/banks/b1/import?dry_run=true"} {
		rec := serve(h, http.MethodPost, path, body, h.ImportTemplate)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Unknown configuration fields: ['totally_unknown']") {
			t.Fatalf("%s: body = %s", path, rec.Body.String())
		}
	}
	bank, err := store.GetBank(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := bank.Config["reflect_mission"]; present {
		t.Fatalf("a refused import must apply nothing: %v", bank.Config)
	}
}

// serveJSON routes one request carrying a JSON body through the handler under
// test, setting the bank path value the real router supplies.
func serveJSON(h *Handlers, method, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("bank_id", "b2")
	rec := httptest.NewRecorder()
	h.ImportTemplate(rec, req)
	return rec
}
