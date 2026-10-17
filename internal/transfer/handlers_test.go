package transfer

import (
	"context"
	"encoding/json"
	"errors"
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
	// reflect_mission is a configuration override, so the applied value is read
	// back from the bank's config column — the same place the config endpoint and
	// the profile overlay consult.
	bank, err := store.GetBank(context.Background(), "b1")
	if err != nil || bank.Config["reflect_mission"] != "help" {
		t.Fatalf("bank config reflect_mission = %#v (err %v)", bank.Config["reflect_mission"], err)
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

// An unparseable dry_run is a request-validation failure, not a silent
// non-dry run: nothing is applied.
func TestImportTemplateRejectsUnparseableDryRun(t *testing.T) {
	h, store := newHandlers(t, true, true)
	body := `{"version":"1","directives":[{"name":"tone","content":"be nice"}]}`
	rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import?dry_run=2", body, h.ImportTemplate)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"bool_parsing"`) {
		t.Fatalf("detail = %s", rec.Body.String())
	}
	if directives, err := store.ListDirectives(context.Background(), "b1"); err != nil || len(directives) != 0 {
		t.Fatalf("a rejected flag must apply nothing: %v (err %v)", directives, err)
	}
}

// A template import applies the manifest model's defaults: an omitted
// is_active is an active directive and an omitted max_tokens is 2048, matching
// the reference's own model defaults.
func TestImportTemplateAppliesModelDefaults(t *testing.T) {
	h, store := newHandlers(t, true, true)
	body := `{"version":"1","mental_models":[{"id":"goals","name":"Goals","source_query":"q"}],"directives":[{"name":"tone","content":"be nice"}]}`
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import", body, h.ImportTemplate); rec.Code != http.StatusOK {
		t.Fatalf("import: %d, body = %s", rec.Code, rec.Body.String())
	}
	model, err := store.GetMentalModel(context.Background(), "b1", "goals")
	if err != nil {
		t.Fatal(err)
	}
	if model.MaxTokens != 2048 {
		t.Fatalf("max_tokens = %d, want 2048", model.MaxTokens)
	}
	directives, err := store.ListDirectives(context.Background(), "b1")
	if err != nil || len(directives) != 1 {
		t.Fatalf("directives = %+v (err %v)", directives, err)
	}
	if !directives[0].IsActive {
		t.Fatal("an omitted is_active must import as an active directive")
	}
	// An explicit is_active=false is still honoured.
	explicit := `{"version":"1","directives":[{"name":"muted","content":"c","is_active":false}]}`
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import", explicit, h.ImportTemplate); rec.Code != http.StatusOK {
		t.Fatalf("import: %d, body = %s", rec.Code, rec.Body.String())
	}
	directives, err = store.ListDirectives(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	for _, directive := range directives {
		if directive.Name == "muted" && directive.IsActive {
			t.Fatal("an explicit is_active=false must be honoured")
		}
	}
}

// The export materialises the model's full trigger, filling the defaults the
// reference's model declares, and exports the whole bank-config model when the
// bank has any stored configuration.
func TestExportMaterialisesTriggerAndConfig(t *testing.T) {
	h, store := newHandlers(t, true, true)
	if _, err := store.PutMentalModel(context.Background(), storage.MentalModel{
		ID: "goals", BankID: "b1", Subtype: storage.SubtypePinned, Name: "Goals",
		SourceQuery: "what are the goals?", Tags: []string{}, MaxTokens: 2048,
		Trigger: map[string]any{"refresh_cron": "0 3 * * *"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateBank(context.Background(), "b1", storage.BankPatch{
		Config: &map[string]any{"retain_mission": "keep facts", "knowledge_page_default_trigger": map[string]any{"mode": "delta", "refresh_cron": nil}},
	}); err != nil {
		t.Fatal(err)
	}

	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export", "", h.ExportTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d, body = %s", rec.Code, rec.Body.String())
	}
	var manifest struct {
		Bank         map[string]any `json:"bank"`
		MentalModels []struct {
			Trigger map[string]any `json:"trigger"`
		} `json:"mental_models"`
		Directives []any `json:"directives"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.MentalModels) != 1 {
		t.Fatalf("models = %+v", manifest.MentalModels)
	}
	trigger := manifest.MentalModels[0].Trigger
	if trigger["mode"] != "full" || trigger["refresh_after_consolidation"] != false ||
		trigger["exclude_mental_models"] != false || trigger["keep_trace"] != false {
		t.Fatalf("trigger defaults not materialised: %v", trigger)
	}
	if trigger["refresh_cron"] != "0 3 * * *" {
		t.Fatalf("stored trigger value must survive: %v", trigger)
	}
	if len(trigger) != len(triggerTemplateFields) {
		t.Fatalf("trigger must carry every declared member, got %d", len(trigger))
	}
	if manifest.Bank == nil {
		t.Fatal("a bank with stored config must export the config section")
	}
	if len(manifest.Bank) != len(bankTemplateFields) {
		t.Fatalf("bank config must carry every declared field, got %d", len(manifest.Bank))
	}
	if manifest.Bank["retain_mission"] != "keep facts" {
		t.Fatalf("stored config value = %v", manifest.Bank["retain_mission"])
	}
	// An unset field is present and null, not omitted.
	if value, present := manifest.Bank["retain_chunk_size"]; !present || value != nil {
		t.Fatalf("unset config field = %v (present %v)", value, present)
	}
}

// A bank with no stored configuration exports no bank section.
func TestExportWithoutConfigHasNullBank(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export", "", h.ExportTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	var manifest struct {
		Bank map[string]any `json:"bank"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Bank != nil {
		t.Fatalf("bank = %v, want null", manifest.Bank)
	}
	if !strings.Contains(rec.Body.String(), `"bank":null`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// A stored config value the template model rejects is dropped rather than
// failing the export.
func TestExportDropsOutOfDomainConfig(t *testing.T) {
	h, store := newHandlers(t, true, true)
	if _, err := store.UpdateBank(context.Background(), "b1", storage.BankPatch{
		Config:      &map[string]any{"retain_mission": "keep"},
		Disposition: &map[string]int{storage.DispositionKeyEmpathy: 0},
	}); err != nil {
		t.Fatal(err)
	}
	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export", "", h.ExportTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d, body = %s", rec.Code, rec.Body.String())
	}
	var manifest struct {
		Bank map[string]any `json:"bank"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Bank["disposition_empathy"] != nil {
		t.Fatalf("an out-of-domain value must be dropped, got %v", manifest.Bank["disposition_empathy"])
	}
	if manifest.Bank["retain_mission"] != "keep" {
		t.Fatalf("the rest of the config must survive: %v", manifest.Bank)
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
	if got := asHTTPError(storage.ErrInvalidBankID); got == nil || got.Status != http.StatusBadRequest {
		t.Fatalf("invalid-bank-id mapped to %+v", got)
	}
	if got := asHTTPError(storage.ErrWritesUnavailable); got == nil || got.Status != http.StatusServiceUnavailable {
		t.Fatalf("writes-unavailable mapped to %+v", got)
	}
	if got := asHTTPError(errors.New("storage exploded")); got == nil || got.Status != http.StatusInternalServerError {
		t.Fatalf("unrecognised error mapped to %+v", got)
	}
}

// The import body is validated by the reference's own model, and its items are
// served as this route renders them: "<loc>: <msg>" joined with "; " under the
// schema-validation prefix. Each expectation is the body the live reference
// serves for the same request.
func TestImportTemplateRejectsBodiesLikeReference(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	cases := []struct{ body, want string }{
		{`{"version":1}`,
			"Template schema validation failed: version: Input should be a valid string"},
		{`{"version":null}`,
			"Template schema validation failed: version: Input should be a valid string"},
		{`{}`,
			"Template schema validation failed: version: Field required"},
		{`{"version":"1","directives":[{"name":"d"}]}`,
			"Template schema validation failed: directives.0.content: Field required"},
		{`{"version":"1","mental_models":[{"id":"X"}]}`,
			"Template schema validation failed: mental_models.0.id: Value error, Mental model id 'X' must be " +
				"alphanumeric lowercase with hyphens, starting with a letter or digit.; " +
				"mental_models.0.name: Field required; mental_models.0.source_query: Field required"},
		{`{"version":"1","mental_models":[{"id":"X","max_tokens":10}]}`,
			"Template schema validation failed: mental_models.0.id: Value error, Mental model id 'X' must be " +
				"alphanumeric lowercase with hyphens, starting with a letter or digit.; " +
				"mental_models.0.name: Field required; mental_models.0.source_query: Field required; " +
				"mental_models.0.max_tokens: Input should be greater than or equal to 256"},
		{`{"version":"1","bank":{"disposition_empathy":9,"retain_chunk_size":"x"}}`,
			"Template schema validation failed: bank.retain_chunk_size: Input should be a valid integer, unable " +
				"to parse string as an integer; bank.disposition_empathy: Input should be less than or equal to 5"},
		{`{"version":"1","bank":123}`,
			"Template schema validation failed: bank: Input should be a valid dictionary or instance of BankTemplateConfig"},
		{`[]`,
			"Template schema validation failed: : Input should be a valid dictionary or instance of BankTemplateManifest"},
	}
	for _, tc := range cases {
		rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import", tc.body, h.ImportTemplate)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", tc.body, rec.Code, rec.Body.String())
		}
		var envelope struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Detail != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.body, envelope.Detail, tc.want)
		}
	}
}

// A member the reference's model accepts is still read as the caller sent it:
// a boolean word for is_active and a numeric string for an integer decode
// rather than being rejected.
func TestImportTemplateAcceptsLaxValues(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	body := `{"version":"1","directives":[{"name":"tone","content":"be nice","is_active":"yes"}]}`
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import?dry_run=true", body, h.ImportTemplate); rec.Code != http.StatusOK {
		t.Fatalf("dry run: %d (body %s)", rec.Code, rec.Body.String())
	}
}
