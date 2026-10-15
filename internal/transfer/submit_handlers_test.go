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
	exportedAt := time.Now().UTC()
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
func TestCloneRefusesExistingTarget(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	seedBank(t, store, "bank-b")

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/bank-a/clone?target_bank_id=bank-b", nil,
		map[string]string{"bank_id": "bank-a"})
	h.SubmitClone(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
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
	if op.Payload["mode"] != "restore" {
		t.Fatalf("mode = %v, want restore", op.Payload["mode"])
	}
	if op.Payload["document_conflict"] != "skip" {
		t.Fatalf("document_conflict = %v, want skip", op.Payload["document_conflict"])
	}
	scope, ok := op.Payload["scope"].(map[string]any)
	if !ok || scope["bank_config"] != true || scope["history"] != false {
		t.Fatalf("scope = %v, want bank_config on and history off", op.Payload["scope"])
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
// import it supersedes, and a disabled gate is indistinguishable from absence.
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
}
