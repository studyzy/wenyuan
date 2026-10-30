package transfer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// refusingImportGate is a deployment's import gate that refuses the named
// document, recording every call so a test can assert what it saw.
type refusingImportGate struct {
	refuse string
	status int
	reason string
	calls  []gateCall
}

type gateCall struct {
	bankID    string
	documents []httpx.ImportDocument
}

func (g *refusingImportGate) ValidateImport(_ context.Context, bankID string, documents []httpx.ImportDocument) httpx.OperationDecision {
	g.calls = append(g.calls, gateCall{bankID: bankID, documents: documents})
	for _, document := range documents {
		if document.Content == g.refuse {
			return httpx.OperationDecision{Allowed: false, Reason: g.reason, Status: g.status}
		}
	}
	return httpx.OperationDecision{Allowed: true}
}

// archiveWithDocuments builds a valid archive holding the given (id, text)
// document payloads.
func archiveWithDocuments(t *testing.T, sourceBank string, documents map[string]string) []byte {
	t.Helper()
	manifest := transferarchive.Manifest{
		SchemaVersion: transferarchive.SupportedSchemaVersion,
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
	w, err := zw.Create(transferarchive.ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(manifestJSON); err != nil {
		t.Fatal(err)
	}
	index := 0
	for id, text := range documents {
		payload, err := json.Marshal(transferarchive.Document{ID: id, OriginalText: text})
		if err != nil {
			t.Fatal(err)
		}
		entry, err := zw.Create(transferarchive.DocumentsDir + "/" + zeroPad(index) + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(payload); err != nil {
			t.Fatal(err)
		}
		index++
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zeroPad renders an archive document ordinal as the reference's entry name
// does.
func zeroPad(index int) string {
	digits := []byte("000000")
	value := []byte{byte('0' + index/100000%10), byte('0' + index/10000%10), byte('0' + index/1000%10),
		byte('0' + index/100%10), byte('0' + index/10%10), byte('0' + index%10)}
	return string(append(digits[:0], value...))
}

// importGateHandlers builds handlers with the given gate installed.
func importGateHandlers(t *testing.T, gate httpx.ImportGate) (*Handlers, *storage.MemoryStore) {
	t.Helper()
	h, store := newTestHandlers(t)
	h.ImportGate = func() httpx.ImportGate { return gate }
	return h, store
}

// assertNothingAccepted checks that a refused submission created no bank,
// queued no operation and stored no archive.
func assertNothingAccepted(t *testing.T, store *storage.MemoryStore, bankID string) {
	t.Helper()
	if _, err := store.GetBank(context.Background(), bankID); err == nil {
		t.Fatalf("a refused import must not create bank %q", bankID)
	}
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: bankID})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("a refused import must queue nothing, got %d operation(s)", len(page.Operations))
	}
	if _, err := store.GetStoredFile(context.Background(), storage.StoredFileKeyForBank(bankID, "document-import.zip")); err == nil {
		t.Fatal("a refused import must store no archive")
	}
	if _, err := store.GetStoredFile(context.Background(), storage.StoredFileKeyForBank(bankID, "transfer-import.zip")); err == nil {
		t.Fatal("a refused import must store no archive")
	}
}

// The document import door asks the deployment's gate about every document of
// the archive, and a refusal is answered before the bank exists, the archive is
// stored or the operation is queued (engine/memory_engine.py:9076-9079,
// :9175-9196; hindsight-system-tests/tests/test_84_import_gate.py).
func TestDocumentImportGateRefusesAndCreatesNothing(t *testing.T) {
	gate := &refusingImportGate{refuse: "OVER-QUOTA: Alice moved to Berlin.", status: 402,
		reason: "over quota"}
	h, store := importGateHandlers(t, gate)

	data := archiveWithDocuments(t, "src", map[string]string{
		"d1": "Alice moved to Berlin.",
		"d2": "OVER-QUOTA: Alice moved to Berlin.",
	})
	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/fresh/document-transfer", body,
		map[string]string{"bank_id": "fresh"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitDocumentImport(rec, req)

	if rec.Code != 402 {
		t.Fatalf("status = %d, want 402 (body %s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"detail":"over quota"`)) {
		t.Fatalf("body = %s, want the validator's reason", rec.Body.String())
	}
	if len(gate.calls) != 1 {
		t.Fatalf("gate calls = %d, want 1", len(gate.calls))
	}
	if gate.calls[0].bankID != "fresh" {
		t.Fatalf("gate bank = %q, want the import's own bank", gate.calls[0].bankID)
	}
	seen := map[string]string{}
	for _, document := range gate.calls[0].documents {
		seen[document.DocumentID] = document.Content
	}
	if len(seen) != 2 || seen["d1"] != "Alice moved to Berlin." || seen["d2"] != "OVER-QUOTA: Alice moved to Berlin." {
		t.Fatalf("gate documents = %v, want every archive document with its id and text", seen)
	}
	assertNothingAccepted(t, store, "fresh")
}

// A gate that allows the archive leaves the import exactly where it was: the
// bank is created, the archive is stored and the operation is queued.
func TestDocumentImportGateAllowsTheImport(t *testing.T) {
	gate := &refusingImportGate{refuse: "never present", status: 402}
	h, store := importGateHandlers(t, gate)

	body, contentType := multipartArchive(t, archiveWithDocuments(t, "src", map[string]string{"d1": "Alice."}))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/fresh/document-transfer", body,
		map[string]string{"bank_id": "fresh"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitDocumentImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	if len(gate.calls) != 1 {
		t.Fatalf("gate calls = %d, want 1", len(gate.calls))
	}
	if _, err := store.GetBank(context.Background(), "fresh"); err != nil {
		t.Fatalf("an allowed import must create the bank: %v", err)
	}
}

// The restore door gates the documents the data section will replay, and its
// refusal creates no target bank and queues nothing
// (engine/memory_engine.py:8244-8252).
func TestRestoreGateRefusesAndCreatesNothing(t *testing.T) {
	gate := &refusingImportGate{refuse: "OVER-QUOTA", status: 402, reason: "over quota"}
	h, store := importGateHandlers(t, gate)
	seedBank(t, store, "src")

	data := archiveWithDocuments(t, "src", map[string]string{"d2": "OVER-QUOTA"})
	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/src/transfer/import?target_bank_id=target", body,
		map[string]string{"bank_id": "src"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != 402 {
		t.Fatalf("status = %d, want 402 (body %s)", rec.Code, rec.Body.String())
	}
	if len(gate.calls) != 1 {
		t.Fatalf("gate calls = %d, want 1", len(gate.calls))
	}
	if gate.calls[0].bankID != "target" {
		t.Fatalf("gate bank = %q, want the restore's target", gate.calls[0].bankID)
	}
	if len(gate.calls[0].documents) != 1 || gate.calls[0].documents[0].Content != "OVER-QUOTA" {
		t.Fatalf("gate documents = %v, want the archive's own documents", gate.calls[0].documents)
	}
	if _, err := store.GetBank(context.Background(), "target"); err == nil {
		t.Fatal("a refused restore must not create the target bank")
	}
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: "src"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("a refused restore must queue nothing, got %d operation(s)", len(page.Operations))
	}
	if _, err := store.GetStoredFile(context.Background(), storage.StoredFileKeyForBank("src", "transfer-import.zip")); err == nil {
		t.Fatal("a refused restore must store no archive")
	}
}

// A restore of the configuration alone replays no document, so the gate is not
// asked about it.
func TestRestoreGateSkippedWithoutTheDataSection(t *testing.T) {
	gate := &refusingImportGate{refuse: "OVER-QUOTA", status: 402}
	h, store := importGateHandlers(t, gate)
	seedBank(t, store, "src")

	data := archiveWithDocuments(t, "src", map[string]string{"d2": "OVER-QUOTA"})
	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost,
		"/v1/default/banks/src/transfer/import?target_bank_id=target&include_data=false", body,
		map[string]string{"bank_id": "src"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	if len(gate.calls) != 0 {
		t.Fatalf("gate calls = %d, want 0: a restore with no data carries no document to judge", len(gate.calls))
	}
}

// A merge on the unified route is the document import, so it is gated too,
// against the bank it merges into.
func TestMergeGateRefusesAndCreatesNothing(t *testing.T) {
	gate := &refusingImportGate{refuse: "OVER-QUOTA", status: 402, reason: "over quota"}
	h, store := importGateHandlers(t, gate)
	seedBank(t, store, "src")

	data := archiveWithDocuments(t, "src", map[string]string{"d2": "OVER-QUOTA"})
	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/merge-target/transfer/import?mode=merge", body,
		map[string]string{"bank_id": "merge-target"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != 402 {
		t.Fatalf("status = %d, want 402 (body %s)", rec.Code, rec.Body.String())
	}
	if len(gate.calls) != 1 || gate.calls[0].bankID != "merge-target" {
		t.Fatalf("gate calls = %+v, want one call for the merge bank", gate.calls)
	}
	assertNothingAccepted(t, store, "merge-target")
}

// A clone assembles its archive in the worker from a bank this instance already
// holds, so no archive passes the gate at submission and the route is not
// gated (engine/memory_engine.py:8295-8320).
func TestCloneIsNotGated(t *testing.T) {
	gate := &refusingImportGate{refuse: "anything", status: 402}
	h, store := importGateHandlers(t, gate)
	seedBank(t, store, "src")

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/src/clone?target_bank_id=copy", nil,
		map[string]string{"bank_id": "src"})
	h.SubmitClone(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	if len(gate.calls) != 0 {
		t.Fatalf("gate calls = %d, want 0: a clone carries no archive at submission", len(gate.calls))
	}
}

// With no gate installed the document import and the restore accept the same
// archive and answer the same bytes as they always have.
func TestImportGateAbsentLeavesSubmissionsUnchanged(t *testing.T) {
	plain, plainStore := newTestHandlers(t)
	gated, gatedStore := importGateHandlers(t, nil)
	seedBank(t, plainStore, "src")
	seedBank(t, gatedStore, "src")

	data := archiveWithDocuments(t, "src", map[string]string{"d1": "Alice."})
	submit := func(h *Handlers, target string) *httptest.ResponseRecorder {
		body, contentType := multipartArchive(t, data)
		rec := httptest.NewRecorder()
		req := routeRequest(http.MethodPost, target, body, map[string]string{"bank_id": "src"})
		req.Header.Set("Content-Type", contentType)
		if target == "/v1/default/banks/src/document-transfer" {
			h.SubmitDocumentImport(rec, req)
			return rec
		}
		h.SubmitTransferImport(rec, req)
		return rec
	}

	for _, target := range []string{
		"/v1/default/banks/src/document-transfer",
		"/v1/default/banks/src/transfer/import?target_bank_id=target",
	} {
		first := submit(plain, target)
		second := submit(gated, target)
		if first.Code != second.Code {
			t.Fatalf("%s: status = %d with no gate, %d with a nil gate", target, first.Code, second.Code)
		}
		// The operation id is freshly generated per submission, so only the
		// accepted shape is compared.
		if first.Code == http.StatusAccepted {
			var firstBody, secondBody operationAccepted
			if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
				t.Fatal(err)
			}
			if firstBody.Status != secondBody.Status {
				t.Fatalf("%s: status member = %q vs %q", target, firstBody.Status, secondBody.Status)
			}
			continue
		}
		if first.Body.String() != second.Body.String() {
			t.Fatalf("%s: body = %s vs %s", target, first.Body.String(), second.Body.String())
		}
	}
}
