package transfer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/banktemplate"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newHandlers builds the transfer handlers over a memory store with one bank.
func newHandlers(t *testing.T, exportEnabled, importEnabled bool) (*Handlers, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	return NewHandlers(store, exportEnabled, importEnabled), store
}

// serve routes one request through the handler under test, setting the bank
// path value the real router supplies.
func serve(h *Handlers, method, path, body string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetPathValue("bank_id", "b1")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

// The export route is the JSON bank-template export, not an archive.
func TestExportReturnsTemplateManifest(t *testing.T) {
	h, store := newHandlers(t, true, true)
	if _, err := store.PutMentalModel(context.Background(), storage.MentalModel{
		ID: "goals", BankID: "b1", Subtype: storage.SubtypePinned, Name: "Goals",
		SourceQuery: "what are the goals?", Tags: []string{}, MaxTokens: 2048,
	}); err != nil {
		t.Fatal(err)
	}

	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export", "", h.ExportTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("export must be JSON, got content type %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("a template export is not a download attachment, got %q", got)
	}
	var manifest banktemplate.Manifest
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != banktemplate.CurrentTemplateVersion {
		t.Fatalf("manifest version = %q, want %q", manifest.Version, banktemplate.CurrentTemplateVersion)
	}
	if len(manifest.MentalModels) != 1 || manifest.MentalModels[0].ID != "goals" {
		t.Fatalf("manifest models = %+v", manifest.MentalModels)
	}
}

// The export route reports not-found for an unknown bank.
func TestExportTemplateUnknownBankIsNotFound(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/default/banks/missing/export", nil)
	req.SetPathValue("bank_id", "missing")
	h.ExportTemplate(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown-bank export = %d, want 404", rec.Code)
	}
}

// The template import validates and, unless it is a dry run, applies the
// manifest's config, mental models and directives.
func TestImportTemplateDryRunAndApply(t *testing.T) {
	h, store := newHandlers(t, true, true)
	body := `{"version":"1","bank":{"reflect_mission":"help"},"mental_models":[{"id":"goals","name":"Goals","source_query":"what goals?"}],"directives":[{"name":"tone","content":"be nice"}]}`

	dry := serve(h, http.MethodPost, "/v1/default/banks/b1/import?dry_run=true", body, h.ImportTemplate)
	if dry.Code != http.StatusOK {
		t.Fatalf("dry run: %d, body = %s", dry.Code, dry.Body.String())
	}
	var dryResult BankTemplateImportResponse
	if err := json.Unmarshal(dry.Body.Bytes(), &dryResult); err != nil {
		t.Fatal(err)
	}
	if !dryResult.DryRun || !dryResult.ConfigApplied || len(dryResult.OperationIDs) != 0 {
		t.Fatalf("dry run result = %+v", dryResult)
	}
	if len(dryResult.MentalModelsCreated) != 1 || dryResult.MentalModelsCreated[0] != "goals" {
		t.Fatalf("dry-run created models = %v", dryResult.MentalModelsCreated)
	}
	if _, err := store.GetMentalModel(context.Background(), "b1", "goals"); err == nil {
		t.Fatal("a dry run must not create anything")
	}

	apply := serve(h, http.MethodPost, "/v1/default/banks/b1/import", body, h.ImportTemplate)
	if apply.Code != http.StatusOK {
		t.Fatalf("apply: %d, body = %s", apply.Code, apply.Body.String())
	}
	var applied BankTemplateImportResponse
	if err := json.Unmarshal(apply.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.ConfigApplied || len(applied.OperationIDs) != 1 {
		t.Fatalf("apply result = %+v", applied)
	}
	if len(applied.MentalModelsCreated) != 1 || applied.MentalModelsCreated[0] != "goals" {
		t.Fatalf("created models = %v", applied.MentalModelsCreated)
	}
	if len(applied.DirectivesCreated) != 1 || applied.DirectivesCreated[0] != "tone" {
		t.Fatalf("created directives = %v", applied.DirectivesCreated)
	}
	if _, err := store.GetMentalModel(context.Background(), "b1", "goals"); err != nil {
		t.Fatalf("the model must be applied: %v", err)
	}
	directives, err := store.ListDirectives(context.Background(), "b1")
	if err != nil || len(directives) != 1 || directives[0].Name != "tone" {
		t.Fatalf("directives = %+v (err %v)", directives, err)
	}
	// reflect_mission is the bank's mission column rather than a stored override,
	// so the applied value is read back from the bank row; the config view that
	// merges it into the overrides is the bank API's, not the raw store's.
	bank, err := store.GetBank(context.Background(), "b1")
	if err != nil || bank.Mission != "help" {
		t.Fatalf("bank mission = %q (err %v)", bank.Mission, err)
	}
	if _, err := store.BankConfig(context.Background(), "b1"); err != nil {
		t.Fatalf("bank config must be readable: %v", err)
	}
}

// A template import creates the bank when it does not exist.
func TestImportTemplateCreatesMissingBank(t *testing.T) {
	h, store := newHandlers(t, true, true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/new-bank/import",
		strings.NewReader(`{"version":"1","directives":[{"name":"tone","content":"be nice"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("bank_id", "new-bank")
	h.ImportTemplate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import into a missing bank = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetBank(context.Background(), "new-bank"); err != nil {
		t.Fatalf("the bank must be created: %v", err)
	}
}

// A manifest that is missing a version, names an unsupported one, carries a
// malformed id or repeats a match key is refused.
func TestImportTemplateValidation(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	cases := []string{
		`{"bank":{"reflect_mission":"x"}}`,
		`{"version":"2"}`,
		`{"version":"abc"}`,
		`{"version":"1","mental_models":[{"id":"Bad_ID","name":"n","source_query":"q"}]}`,
		`{"version":"1","directives":[{"name":"d","content":"c"},{"name":"d","content":"c2"}]}`,
		`not json`,
	}
	for _, body := range cases {
		if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import", body, h.ImportTemplate); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

// The template routes are a bank configuration surface, not the gated document
// import: a disabled document-import gate leaves them available.
func TestTemplateRoutesIgnoreDocumentImportGate(t *testing.T) {
	h, _ := newHandlers(t, false, false)
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import", `{"version":"1"}`, h.ImportTemplate); rec.Code != http.StatusOK {
		t.Fatalf("template import under a closed document gate: %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export", "", h.ExportTemplate); rec.Code != http.StatusOK {
		t.Fatalf("template export under a closed document gate: %d, want 200", rec.Code)
	}
}

// The route table declares the family's public contract, and the download
// pattern is the one non-bank-scoped route.
func TestTransferRouteTable(t *testing.T) {
	routes := Routes()
	if len(routes) == 0 {
		t.Fatal("the transfer route table is empty")
	}
	seen := map[string]bool{}
	for _, route := range routes {
		seen[route.Method+" "+route.Pattern] = true
	}
	if !seen[http.MethodPost+" /v1/default/banks/{bank_id}/import"] {
		t.Fatalf("the import route is missing from the table: %v", seen)
	}
	if !strings.HasPrefix(DownloadPattern(), "/v1/default/files/download/") {
		t.Fatalf("download pattern = %q", DownloadPattern())
	}
}

// asHTTPError maps the shared storage sentinels onto their envelopes.
func TestTransferErrorMapping(t *testing.T) {
	if asHTTPError(nil) != nil {
		t.Fatal("a nil error must map to nil")
	}
	if got := asHTTPError(storage.ErrBankNotFound); got == nil || got.Status != http.StatusNotFound {
		t.Fatalf("bank-not-found mapped to %+v", got)
	}
}
