package transfer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newTestHandlers builds handlers over an in-memory store with both document
// gates enabled.
func newTestHandlers(t *testing.T) (*Handlers, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	return NewHandlers(store, true, true), store
}

// routeRequest builds a request carrying the path values the router would
// normally supply, so a handler can be exercised without the full mux.
func routeRequest(method, target string, body *bytes.Buffer, params map[string]string) *http.Request {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, body)
	}
	for name, value := range params {
		req.SetPathValue(name, value)
	}
	return req
}

// seedBank creates a bank so a bank-scoped route authorizes.
func seedBank(t *testing.T, store *storage.MemoryStore, bankID string) {
	t.Helper()
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bankID, Name: bankID}); err != nil {
		t.Fatalf("seed bank %q: %v", bankID, err)
	}
}

// operationByID reads one recorded operation back by listing the bank's
// operations, so the helper is independent of the single-operation accessor's
// argument shape.
func operationByID(store *storage.MemoryStore, bankID, opID string) (storage.Operation, error) {
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: bankID})
	if err != nil {
		return storage.Operation{}, err
	}
	for _, op := range page.Operations {
		if op.OperationID == opID {
			return op, nil
		}
	}
	return storage.Operation{}, fmt.Errorf("operation %s not found", opID)
}

// archiveBytes builds a minimal valid archive payload.
func archiveBytes(t *testing.T, sourceBank string) []byte {
	t.Helper()
	manifest := hindsightarchive.Manifest{
		SchemaVersion: hindsightarchive.SupportedSchemaVersion,
		SourceBankID:  sourceBank,
		ArchiveType:   "bank",
	}
	exportedAt := httpx.FormatZuluTimestamp(time.Now().UTC())
	manifest.ExportedAt = &exportedAt
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(hindsightarchive.ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifestJSON); err != nil {
		t.Fatal(err)
	}
	// The reader requires at least the documents directory to exist, so the
	// fixture carries one document payload named by zero-padded ordinal.
	doc, err := zw.Create(hindsightarchive.DocumentsDir + "/000000.json")
	if err != nil {
		t.Fatal(err)
	}
	documentJSON, err := json.Marshal(hindsightarchive.Document{ID: "doc-1", OriginalText: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := doc.Write(documentJSON); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// multipartArchive wraps archive bytes in a multipart file field.
func multipartArchive(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "archive.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

// test-submit-accepted-shape: every asynchronous submit answers the accepted
// status with exactly the operation id and the pending status.
func TestSubmitAcceptedCarriesOnlyOperationIDAndStatus(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/export", nil,
		map[string]string{"bank_id": "bank-a"})
	h.SubmitTransferExport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if len(payload) != 2 {
		t.Fatalf("body must carry exactly two fields, got %v", payload)
	}
	if payload["operation_id"] == "" || payload["status"] != string(storage.StatusPending) {
		t.Fatalf("unexpected body %v", payload)
	}
}

// test-retired-read-always-gone: the removed synchronous document read is
// terminal and performs no work.
func TestRetiredDocumentTransferIsGone(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodGet, "/v1/default/banks/bank-a/document-transfer", nil,
		map[string]string{"bank_id": "bank-a"})
	h.ServeRetiredDocumentTransfer(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusGone)
	}
	if !strings.Contains(rec.Body.String(), "download") {
		t.Fatalf("gone detail must name where to go instead: %s", rec.Body.String())
	}
}

// test-unknown-bank-not-found: a bank-scoped route on an unknown bank is a
// not-found caller error.
func TestUnknownBankIsNotFound(t *testing.T) {
	h, _ := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/missing/transfer/export", nil,
		map[string]string{"bank_id": "missing"})
	h.SubmitTransferExport(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// test-document-gate-disabled-is-not-found: a disabled gate is
// indistinguishable from an absent route.
func TestDisabledDocumentGateIsNotFound(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(store, false, false)
	seedBank(t, store, "bank-a")

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/document-transfer/export", nil,
		map[string]string{"bank_id": "bank-a"})
	h.SubmitDocumentTransferExport(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// test-clone-refuses-self: a bank cannot be cloned onto itself.
func TestCloneRefusesSelfTarget(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/clone?target_bank_id=bank-a", nil,
		map[string]string{"bank_id": "bank-a"})
	h.SubmitClone(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

// test-clone-refuses-existing-target: clone is refused when the target exists.
// The reference answers 400 (not a conflict) with a message naming the target.
func TestCloneRefusesExistingTarget(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	seedBank(t, store, "bank-b")

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/clone?target_bank_id=bank-b", nil,
		map[string]string{"bank_id": "bank-a"})
	h.SubmitClone(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("detail must name the conflict: %s", rec.Body.String())
	}
}

// test-merge-rejects-target-and-include: merge takes the archive's documents
// only, so a target or an include flag is incoherent with it.
func TestMergeRejectsTargetAndIncludeFlags(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	data := archiveBytes(t, "bank-a")

	cases := []string{
		"?mode=merge&target_bank_id=other",
		"?mode=merge&include_data=true",
	}
	for _, suffix := range cases {
		body, contentType := multipartArchive(t, data)
		rec := httptest.NewRecorder()
		req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/import"+suffix, body,
			map[string]string{"bank_id": "bank-a"})
		req.Header.Set("Content-Type", contentType)
		h.SubmitTransferImport(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want %d (body %s)", suffix, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	}
}

// test-import-validates-synchronously: a malformed archive fails immediately
// rather than being queued.
func TestMalformedArchiveRejectedSynchronously(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	body, contentType := multipartArchive(t, []byte("not a zip"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/import", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	// Nothing may be queued for a rejected archive.
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: "bank-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("a rejected archive must not queue work, got %d operations", len(page.Operations))
	}
}

// test-upload-stashed-under-bank-key: the queued payload carries only the
// storage key, and the bytes live under a bank-scoped key.
func TestUploadIsStashedUnderBankScopedKey(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	data := archiveBytes(t, "fresh-bank")

	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/import?mode=restore&include_data=true", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var payload struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	op, err := operationByID(store, "bank-a", payload.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := op.Payload[storage.OperationPayloadKeyStoredFile].(string)
	if !ok || key == "" {
		t.Fatalf("payload must carry the stored key, got %v", op.Payload)
	}
	// The key must resolve to the owning bank and the bytes must be readable.
	bankID, ok := storage.BankIDFromStoredFileKey(key)
	if !ok || bankID != "bank-a" {
		t.Fatalf("key %q must resolve to bank-a", key)
	}
	file, err := store.GetStoredFile(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(file.Data, data) {
		t.Fatal("stored payload does not match the upload")
	}
}

// test-import-defaults: a transfer import body that omits mode, document
// conflict and the include flags is a whole-bank restore with the reference's
// defaults (data + bank config on, history off), not an error.
func TestTransferImportAppliesReferenceDefaults(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	// The archive names a bank that does not exist yet: a restore creates its
	// target, and restoring onto an existing bank is refused.
	data := archiveBytes(t, "fresh-bank")

	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/import", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var payload struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	op, err := operationByID(store, "bank-a", payload.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationType != storage.OperationTypeTransferImport {
		t.Fatalf("operation type = %q, want %q", op.OperationType, storage.OperationTypeTransferImport)
	}
	if op.Payload["target_bank_id"] != "fresh-bank" {
		t.Fatalf("target_bank_id = %v, want fresh-bank", op.Payload["target_bank_id"])
	}
	if op.Payload["include_data"] != true || op.Payload["include_bank_config"] != true || op.Payload["include_history"] != false {
		t.Fatalf("scope payload = %v, want data & bank_config on and history off", op.Payload)
	}
	if _, present := op.Payload["mode"]; present {
		t.Fatalf("a restore payload must not carry a mode member: %v", op.Payload)
	}
}

// test-import-rejects-invalid-mode-and-conflict: the mode and document-conflict
// vocabularies are validated, not accepted blindly.
func TestTransferImportRejectsInvalidModeAndConflict(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	data := archiveBytes(t, "bank-a")

	for _, suffix := range []string{"?mode=bogus", "?document_conflict=bogus"} {
		body, contentType := multipartArchive(t, data)
		rec := httptest.NewRecorder()
		req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/import"+suffix, body,
			map[string]string{"bank_id": "bank-a"})
		req.Header.Set("Content-Type", contentType)
		h.SubmitTransferImport(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", suffix, rec.Code, rec.Body.String())
		}
	}
}

// test-import-gate-disabled: the transfer import is gated like the document
// import it supersedes, and the detail names the flag that enables it.
func TestTransferImportGateDisabledIsNotFound(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(store, true, false)
	seedBank(t, store, "bank-a")

	body, contentType := multipartArchive(t, archiveBytes(t, "bank-a"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/import", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "HINDSIGHT_API_ENABLE_DOCUMENT_IMPORT_API") {
		t.Fatalf("disabled detail must name the flag: %s", rec.Body.String())
	}
}

// test-transfer-export-refuses-empty-scope: a request that would carry nothing
// is refused with 400 before any bank lookup, so it wins over a missing bank.
func TestTransferExportRefusesEmptyScope(t *testing.T) {
	h, _ := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost,
		"/v1/default/banks/missing/transfer/export?include_data=false&include_bank_config=false&include_history=false",
		nil, map[string]string{"bank_id": "missing"})
	h.SubmitTransferExport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Nothing to export") {
		t.Fatalf("detail = %s", rec.Body.String())
	}
}

// test-export-flag-rejects-unparseable-token: an invalid boolean token is a
// request-validation failure, not a silent false.
func TestExportFlagRejectsUnparseableToken(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/export?include_data=banana",
		nil, map[string]string{"bank_id": "bank-a"})
	h.SubmitTransferExport(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"bool_parsing"`) {
		t.Fatalf("detail = %s", rec.Body.String())
	}
	// The reference's extra spellings ("t"/"y") still parse as true.
	ok := httptest.NewRecorder()
	req2 := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/export?include_data=t",
		nil, map[string]string{"bank_id": "bank-a"})
	h.SubmitTransferExport(ok, req2)
	if ok.Code != http.StatusAccepted {
		t.Fatalf("t must parse as true: %d (body %s)", ok.Code, ok.Body.String())
	}
}

// test-clone-refuses-empty-scope: a clone that inherits nothing is refused.
func TestCloneRefusesEmptyScope(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost,
		"/v1/default/banks/bank-a/clone?target_bank_id=dst&include_data=false&include_bank_config=false&include_history=false",
		nil, map[string]string{"bank_id": "bank-a"})
	h.SubmitClone(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Nothing to clone") {
		t.Fatalf("detail = %s", rec.Body.String())
	}
}

// test-clone-requires-target: the absent required query parameter is a
// request-validation failure, not a caller error.
func TestCloneRequiresTarget(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/clone", nil,
		map[string]string{"bank_id": "bank-a"})
	h.SubmitClone(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"target_bank_id"`) {
		t.Fatalf("detail = %s", rec.Body.String())
	}
}

// test-clone-default-flags-carry-config: with no include flags the clone
// inherits the bank's configuration, so the queued payload says so.
func TestCloneDefaultFlagsCarryBankConfig(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/clone?target_bank_id=dst", nil,
		map[string]string{"bank_id": "bank-a"})
	h.SubmitClone(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	op, err := operationByID(store, "bank-a", payload.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Payload["include_bank_config"] != true || op.Payload["include_data"] != true {
		t.Fatalf("default clone must carry data and bank config: %v", op.Payload)
	}
}

// test-clone-validates-target-id: an id that must not be created is refused
// with the reference's wording.
func TestCloneValidatesTargetID(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	cases := []struct {
		target string
		want   string
	}{
		{strings.Repeat("a", 200), "too long"},
		{"a%01b", "control characters"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/clone?target_bank_id="+tc.target,
			nil, map[string]string{"bank_id": "bank-a"})
		h.SubmitClone(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("target %q: status = %d body = %s", tc.target, rec.Code, rec.Body.String())
		}
	}
}

// test-gate-disabled-details: each gated route names the flag that enables it.
func TestGateDisabledDetails(t *testing.T) {
	store := storage.NewMemoryStore("public")
	seedBank(t, store, "bank-a")

	export := NewHandlers(store, false, true)
	rec := httptest.NewRecorder()
	export.SubmitTransferExport(rec, routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/export",
		nil, map[string]string{"bank_id": "bank-a"}))
	if !strings.Contains(rec.Body.String(), "Bank export API is disabled") {
		t.Fatalf("transfer export detail = %s", rec.Body.String())
	}

	docExport := NewHandlers(store, false, true)
	rec = httptest.NewRecorder()
	docExport.SubmitDocumentTransferExport(rec, routeRequest(http.MethodPost,
		"/v1/default/banks/bank-a/document-transfer/export", nil, map[string]string{"bank_id": "bank-a"}))
	if !strings.Contains(rec.Body.String(), "Document export API is disabled") {
		t.Fatalf("document export detail = %s", rec.Body.String())
	}

	clone := NewHandlers(store, false, false)
	rec = httptest.NewRecorder()
	clone.SubmitClone(rec, routeRequest(http.MethodPost, "/v1/default/banks/bank-a/clone?target_bank_id=dst",
		nil, map[string]string{"bank_id": "bank-a"}))
	if !strings.Contains(rec.Body.String(), "Bank clone API is disabled") ||
		!strings.Contains(rec.Body.String(), "HINDSIGHT_API_ENABLE_DOCUMENT_EXPORT_API") {
		t.Fatalf("clone detail = %s", rec.Body.String())
	}

	docImport := NewHandlers(store, true, false)
	body, contentType := multipartArchive(t, archiveBytes(t, "bank-a"))
	rec = httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/document-transfer", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	docImport.SubmitDocumentImport(rec, req)
	if !strings.Contains(rec.Body.String(), "Document import API is disabled") {
		t.Fatalf("document import detail = %s", rec.Body.String())
	}
}

// test-transfer-export-subset-refuses-default-bank-config: a document subset
// carries no bank-level section, and include_bank_config defaults on, so a bare
// document_id is refused exactly as the reference refuses it.
func TestTransferExportSubsetRefusesDefaultBankConfig(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/export?document_id=doc-1",
		nil, map[string]string{"bank_id": "bank-a"})
	h.SubmitTransferExport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	want := `{"detail":"include_bank_config and include_history are only supported for a whole-bank export (omit document_id)"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("detail = %s", got)
	}
}

// test-retired-detail-names-the-replacement: the gone message spells out the
// endpoint, the poll URL and the field that carries the download.
func TestRetiredDetailNamesReplacement(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	h.ServeRetiredDocumentTransfer(rec, routeRequest(http.MethodGet, "/v1/default/banks/bank-a/document-transfer",
		nil, map[string]string{"bank_id": "bank-a"}))
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"document-transfer/export", "/operations/{operation_id}", "result_metadata", "bank-a"} {
		if !strings.Contains(body, want) {
			t.Fatalf("retired detail must contain %q: %s", want, body)
		}
	}
}

// test-import-rejects-padded-mode-verbatim: the enum is matched verbatim, so a
// padded value is refused rather than trimmed and accepted.
func TestImportRejectsPaddedModeVerbatim(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	body, contentType := multipartArchive(t, archiveBytes(t, "bank-a"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/import?mode=%20restore%20", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Invalid mode ' restore '") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// test-document-import-rejects-empty-conflict: a present empty on_conflict is
// not the default; it is refused.
func TestDocumentImportRejectsEmptyConflict(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	body, contentType := multipartArchive(t, archiveBytes(t, "bank-a"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/document-transfer?on_conflict=", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitDocumentImport(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Invalid on_conflict ''") {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

// test-document-export-rejects-subset-with-observations: the subset and a
// bank-level section are refused together, byte-identically to the reference.
func TestDocumentExportRejectsSubsetWithObservations(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost,
		"/v1/default/banks/bank-a/document-transfer/export?document_id=d1&include_observations=true",
		nil, map[string]string{"bank_id": "bank-a"})
	h.SubmitDocumentTransferExport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	want := `{"detail":"include_observations and include_knowledge_base are only supported when exporting the whole bank (omit document_id)"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("detail = %s", got)
	}
}

// test-transfer-export-subset-records-document-export: a document subset
// records the document-export operation the reference records.
func TestTransferExportSubsetRecordsDocumentExport(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost,
		"/v1/default/banks/bank-a/transfer/export?document_id=d1&include_bank_config=false",
		nil, map[string]string{"bank_id": "bank-a"})
	h.SubmitTransferExport(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	op, err := operationByID(store, "bank-a", payload.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationType != storage.OperationTypeDocumentExport {
		t.Fatalf("operation type = %q, want %q", op.OperationType, storage.OperationTypeDocumentExport)
	}
}

// test-import-restore-onto-existing-names-the-target: the conflict detail is
// the reference's sentence.
func TestImportRestoreOntoExistingNamesTarget(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	seedBank(t, store, "target-bank")
	body, contentType := multipartArchive(t, archiveBytes(t, "bank-a"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost,
		"/v1/default/banks/bank-a/transfer/import?mode=restore&target_bank_id=target-bank", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Target bank 'target-bank' already exists") {
		t.Fatalf("detail = %s", rec.Body.String())
	}
}

// test-rejected-import-does-not-create-the-bank: a malformed archive must not
// leave an empty bank behind.
func TestRejectedImportDoesNotCreateTheBank(t *testing.T) {
	h, store := newTestHandlers(t)
	body, contentType := multipartArchive(t, []byte("not a zip"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/fresh-bank/document-transfer", body,
		map[string]string{"bank_id": "fresh-bank"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitDocumentImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if _, err := store.GetBank(context.Background(), "fresh-bank"); err == nil {
		t.Fatal("a rejected import must not create the bank")
	}
}

// zipWithoutManifest builds a readable zip that is not a transfer archive: it
// has an entry but no manifest.json.
func zipWithoutManifest(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("just a file")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// test-archive-error-wording: a malformed archive carries the reference's own
// wording, naming the endpoint a valid archive comes from.
func TestArchiveErrorsUseEndpointWording(t *testing.T) {
	const nonZip = "Invalid transfer archive: the uploaded file is not a readable .zip (File is not a zip file)"
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	noManifest := zipWithoutManifest(t)

	documentCases := []struct {
		name string
		body []byte
		want string
	}{
		{"non-zip", []byte("nope"), nonZip},
		{"missing manifest", noManifest,
			"Invalid transfer archive: manifest.json is missing. This endpoint only accepts a .zip produced by the document export endpoint"},
	}
	for _, tc := range documentCases {
		body, contentType := multipartArchive(t, tc.body)
		rec := httptest.NewRecorder()
		req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/document-transfer", body,
			map[string]string{"bank_id": "bank-a"})
		req.Header.Set("Content-Type", contentType)
		h.SubmitDocumentImport(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("document %s: status = %d, want 400 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("document %s: detail = %s, want %q", tc.name, rec.Body.String(), tc.want)
		}
	}

	bankCases := []struct {
		name string
		body []byte
		want string
	}{
		{"non-zip", []byte("nope"), nonZip},
		{"missing manifest", noManifest,
			"Invalid transfer archive: manifest.json is missing. This endpoint only accepts a .zip produced by the bank export endpoint"},
	}
	for _, tc := range bankCases {
		body, contentType := multipartArchive(t, tc.body)
		rec := httptest.NewRecorder()
		req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/transfer/import", body,
			map[string]string{"bank_id": "bank-a"})
		req.Header.Set("Content-Type", contentType)
		h.SubmitTransferImport(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("bank %s: status = %d, want 400 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("bank %s: detail = %s, want %q", tc.name, rec.Body.String(), tc.want)
		}
	}
}

// test-import-unknown-bank-is-not-created: a restore records the operation
// against the bank in the path, which must already exist; an unknown one is
// answered not-found rather than created as a side effect of the submit.
func TestTransferImportUnknownBankIsNotFound(t *testing.T) {
	h, store := newTestHandlers(t)
	body, contentType := multipartArchive(t, archiveBytes(t, "src-bank"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost,
		"/v1/default/banks/missing-bank/transfer/import?target_bank_id=fresh-target", body,
		map[string]string{"bank_id": "missing-bank"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if _, err := store.GetBank(context.Background(), "missing-bank"); err == nil {
		t.Fatal("a restore into an unknown bank must not create it")
	}
}

// test-import-merge-validates-archive-first: a merge parses its archive before
// creating the bank, so a malformed one leaves no bank behind.
func TestTransferImportMergeValidatesArchiveBeforeCreatingTheBank(t *testing.T) {
	h, store := newTestHandlers(t)
	body, contentType := multipartArchive(t, []byte("not a zip"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/merge-bank/transfer/import?mode=merge", body,
		map[string]string{"bank_id": "merge-bank"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if _, err := store.GetBank(context.Background(), "merge-bank"); err == nil {
		t.Fatal("a rejected merge must not create the bank")
	}
}

// test-document-import-payload: the queued document-import payload carries the
// reference's members — the archive key and the conflict policy — under an
// import_documents operation.
func TestDocumentImportPayloadMatchesReference(t *testing.T) {
	h, store := newTestHandlers(t)
	body, contentType := multipartArchive(t, archiveBytes(t, "bank-a"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/document-transfer?on_conflict=replace", body,
		map[string]string{"bank_id": "bank-a"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitDocumentImport(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	op, err := operationByID(store, "bank-a", accepted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationType != storage.OperationTypeDocumentImport {
		t.Fatalf("operation type = %q, want %q", op.OperationType, storage.OperationTypeDocumentImport)
	}
	if op.Payload["on_conflict"] != "replace" {
		t.Fatalf("on_conflict = %v, want replace", op.Payload["on_conflict"])
	}
	if key, _ := op.Payload[storage.OperationPayloadKeyStoredFile].(string); key == "" {
		t.Fatalf("payload must carry the stored key: %v", op.Payload)
	}
	if _, present := op.Payload["mode"]; present {
		t.Fatalf("a document-import payload must not carry a mode member: %v", op.Payload)
	}
}

// test-transfer-import-merge-recorded-as-document-import: a merge through the
// transfer route records the same import_documents payload the document import
// does, and creates the bank it writes into.
func TestTransferImportMergeRecordsDocumentImport(t *testing.T) {
	h, store := newTestHandlers(t)
	body, contentType := multipartArchive(t, archiveBytes(t, "src-bank"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost,
		"/v1/default/banks/merge-bank/transfer/import?mode=merge&document_conflict=replace", body,
		map[string]string{"bank_id": "merge-bank"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	var accepted struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	op, err := operationByID(store, "merge-bank", accepted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationType != storage.OperationTypeDocumentImport {
		t.Fatalf("operation type = %q, want %q", op.OperationType, storage.OperationTypeDocumentImport)
	}
	if op.Payload["on_conflict"] != "replace" {
		t.Fatalf("on_conflict = %v, want replace", op.Payload["on_conflict"])
	}
	if _, err := store.GetBank(context.Background(), "merge-bank"); err != nil {
		t.Fatalf("a merge creates the bank it writes into: %v", err)
	}
}

// test-missing-file-is-a-validation-failure: the archive is a required
// multipart field on both import routes, so a request without one answers the
// framework's 422 envelope naming the missing field — not a 400 caller error.
func TestMissingUploadFileIsValidationFailure(t *testing.T) {
	h, _ := newTestHandlers(t)
	cases := []struct {
		name   string
		submit func(http.ResponseWriter, *http.Request)
		path   string
	}{
		{"document import", h.SubmitDocumentImport, "/v1/default/banks/bank-a/document-transfer"},
		{"transfer import", h.SubmitTransferImport, "/v1/default/banks/bank-a/transfer/import"},
	}
	const want = `{"detail":[{"type":"missing","loc":["body","file"],"msg":"Field required","input":null}]}`
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		req := routeRequest(http.MethodPost, tc.path, nil, map[string]string{"bank_id": "bank-a"})
		tc.submit(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 422 (body %s)", tc.name, rec.Code, rec.Body.String())
		}
		if body := strings.TrimSpace(rec.Body.String()); body != want {
			t.Fatalf("%s: body = %s, want %s", tc.name, body, want)
		}
	}
}
