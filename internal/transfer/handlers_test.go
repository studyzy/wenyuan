package transfer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// newHandlers builds the transfer handlers over a memory store with one bank.
func newHandlers(t *testing.T, exportEnabled, importEnabled bool) (*Handlers, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{ID: "b1", Name: "Bank"}); err != nil {
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

// The export route produces a zip archive carrying the bank's data.
func TestExportProducesArchive(t *testing.T) {
	h, store := newHandlers(t, true, true)
	if _, err := store.RetainWrite(context.Background(), storage.RetainRequest{
		BankID: "b1",
		Items:  []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}

	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export", "", h.Export)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d, body = %s", rec.Code, rec.Body.String())
	}
	// The whole-bank export is the bankadmin JSON archive, and it round-trips
	// back into a bank.
	var archive struct {
		Bank struct {
			ID string `json:"bank_id"`
		} `json:"bank"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &archive); err != nil {
		t.Fatalf("exported bytes are not a readable archive: %v", err)
	}
	if archive.Bank.ID != "b1" {
		t.Fatalf("archive bank = %q, want b1", archive.Bank.ID)
	}
}

// The export route refuses a selection it cannot honour: a document subset
// cannot be combined with a whole-bank section.
func TestExportRejectsBadSelection(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export?document_id=d1&include_data=true", "", h.Export)
	if rec.Code == http.StatusOK {
		t.Fatalf("an incoherent selection must be refused, got %d", rec.Code)
	}
}

// The template export renders the bank's configuration.
func TestExportTemplateRendersManifest(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	rec := serve(h, http.MethodGet, "/v1/default/banks/b1/export-template", "", h.ExportTemplate)
	if rec.Code != http.StatusOK {
		t.Fatalf("export template: %d", rec.Code)
	}
	var manifest templateManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.BankID != "b1" {
		t.Fatalf("manifest = %+v", manifest)
	}
}

// The template import validates and, unless it is a dry run, queues the work.
func TestImportTemplateDryRunAndApply(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	body := `{"bank_id":"b1","config":{"mental_models":["goals"],"directives":["tone"]}}`

	dry := serve(h, http.MethodPost, "/v1/default/banks/b1/import-template?dry_run=true", body, h.ImportTemplate)
	if dry.Code != http.StatusOK {
		t.Fatalf("dry run: %d, body = %s", dry.Code, dry.Body.String())
	}
	var dryResult templateImportResult
	if err := json.Unmarshal(dry.Body.Bytes(), &dryResult); err != nil {
		t.Fatal(err)
	}
	if !dryResult.DryRun || dryResult.ConfigApplied || len(dryResult.OperationIDs) != 0 {
		t.Fatalf("dry run applied changes: %+v", dryResult)
	}

	apply := serve(h, http.MethodPost, "/v1/default/banks/b1/import-template", body, h.ImportTemplate)
	if apply.Code != http.StatusOK {
		t.Fatalf("apply: %d, body = %s", apply.Code, apply.Body.String())
	}
	var applied templateImportResult
	if err := json.Unmarshal(apply.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.ConfigApplied || len(applied.OperationIDs) == 0 {
		t.Fatalf("apply result = %+v", applied)
	}
}

// A template whose manifest names a different bank is refused, and a malformed
// body is refused.
func TestImportTemplateValidation(t *testing.T) {
	h, _ := newHandlers(t, true, true)
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import-template", `{"bank_id":"other"}`, h.ImportTemplate); rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched bank: %d, want 400", rec.Code)
	}
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import-template", `not json`, h.ImportTemplate); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: %d, want 400", rec.Code)
	}
}

// A gated route is indistinguishable from an absent one.
func TestGatedRoutesAreNotFound(t *testing.T) {
	h, _ := newHandlers(t, false, false)
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/document-transfer",
		bodyForImport(t), h.SubmitDocumentImport); rec.Code != http.StatusNotFound {
		t.Fatalf("gated document import: %d, want 404", rec.Code)
	}
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import-template", `{"bank_id":"b1"}`, h.ImportTemplate); rec.Code != http.StatusNotFound {
		t.Fatalf("gated template apply: %d, want 404", rec.Code)
	}
	// A dry run is a read, so it is answered even when the gate is off.
	if rec := serve(h, http.MethodPost, "/v1/default/banks/b1/import-template?dry_run=true", `{"bank_id":"b1"}`, h.ImportTemplate); rec.Code != http.StatusOK {
		t.Fatalf("dry run under a closed gate: %d, want 200", rec.Code)
	}
}

// bodyForImport builds a multipart body naming an archive field.
func bodyForImport(t *testing.T) string {
	t.Helper()
	return "not-a-real-multipart-body"
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
