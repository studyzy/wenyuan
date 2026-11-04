package transfer

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// documentsArchive is an archive a document export produces: the same manifest
// shape as archiveWithDocuments, but declaring archive_type "documents".
func documentsArchive(t *testing.T, sourceBank string) []byte {
	t.Helper()
	manifest := transferarchive.Manifest{
		SchemaVersion: transferarchive.SupportedSchemaVersion,
		SourceBankID:  sourceBank,
		ArchiveType:   "documents",
	}
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
	payload, err := json.Marshal(transferarchive.Document{ID: "d1", OriginalText: "Alice."})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := zw.Create(transferarchive.DocumentsDir + "/000000.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// submitRestore posts a multipart restore of data to route and returns the
// recorder.
func submitRestore(t *testing.T, h *Handlers, route string, data []byte, recordedBank string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, route, body, map[string]string{"bank_id": recordedBank})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)
	return rec
}

// The reference only reads a restore's manifest when it must derive the target
// from it: `target = target_bank_id or read_bank_manifest(archive).source_bank_id`
// (engine/memory_engine.py:8229) short-circuits on a supplied target, so with
// target_bank_id the archive is never parsed and the submission is accepted
// unvalidated — a documents archive, or bytes that are not a zip, both answer
// 202. Without it, read_bank_manifest refuses archive_type != "bank"
// (engine/transfer/importer.py:308-313).
//
// Falsify by parsing and type-checking the archive unconditionally: the
// target-supplied rows below answer the documents-archive refusal instead of
// 202 and fail.
func TestRestoreValidatesTheArchiveOnlyWhenItMustReadTheManifest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		route      string
		data       func(t *testing.T) []byte
		wantStatus int
		wantDetail string
	}{
		{
			name:       "documents archive, target supplied: never read, accepted",
			route:      "/v1/default/banks/src/transfer/import?target_bank_id=fresh",
			data:       func(t *testing.T) []byte { return documentsArchive(t, "src") },
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "not a zip at all, target supplied: never read, accepted",
			route:      "/v1/default/banks/src/transfer/import?target_bank_id=fresh",
			data:       func(*testing.T) []byte { return []byte(`{"version":"1"}`) },
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "documents archive, no target: manifest read, refused",
			route:      "/v1/default/banks/src/transfer/import",
			data:       func(t *testing.T) []byte { return documentsArchive(t, "src") },
			wantStatus: http.StatusBadRequest,
			wantDetail: "Not a whole-bank archive (archive_type='documents'); use import_documents instead",
		},
		{
			name:       "not a zip at all, no target: manifest read, refused",
			route:      "/v1/default/banks/src/transfer/import",
			data:       func(*testing.T) []byte { return []byte(`{"version":"1"}`) },
			wantStatus: http.StatusBadRequest,
			wantDetail: "Invalid transfer archive: the uploaded file is not a readable .zip (File is not a zip file)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newTestHandlers(t)
			seedBank(t, store, "src")
			rec := submitRestore(t, h, tc.route, tc.data(t), "src")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantDetail == "" {
				return
			}
			var envelope struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode body: %v (body %s)", err, rec.Body.String())
			}
			if envelope.Detail != tc.wantDetail {
				t.Fatalf("detail = %q, want %q", envelope.Detail, tc.wantDetail)
			}
		})
	}
}

// The target-existence check still runs when a target is supplied: the
// reference validates target_bank_id (engine/memory_engine.py:8234) before the
// task is queued.
func TestRestoreStillRefusesAnExistingTarget(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "src")
	seedBank(t, store, "taken")

	rec := submitRestore(t, h, "/v1/default/banks/src/transfer/import?target_bank_id=taken",
		documentsArchive(t, "src"), "src")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := "Target bank 'taken' already exists; a bank restore writes into a fresh bank (it is not a merge). " +
		"Delete it first, or choose a different target bank id."
	if envelope.Detail != want {
		t.Fatalf("detail = %q, want %q", envelope.Detail, want)
	}
}

// An archive that predates archive_type is still a whole-bank archive: every
// bank export before the field existed omitted it.
func TestRequireBankArchiveAcceptsMissingType(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typeName string
		wantErr  bool
	}{
		{name: "absent", typeName: "", wantErr: false},
		{name: "bank", typeName: "bank", wantErr: false},
		{name: "documents", typeName: "documents", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireBankArchive(&transferarchive.Manifest{ArchiveType: tc.typeName})
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// The merge mode of the same route is the document import, so it must keep
// accepting the documents archive a manifest-less restore refuses.
func TestMergeStillAcceptsDocumentsArchive(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "merge-target")

	body, contentType := multipartArchive(t, documentsArchive(t, "src"))
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/merge-target/transfer/import?mode=merge", body,
		map[string]string{"bank_id": "merge-target"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
}
