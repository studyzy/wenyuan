package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// importGateValidator is a deployment validator that carries the import gate:
// it refuses any archive holding a document whose text names overQuota.
type importGateValidator struct {
	overQuota string
	status    int
	calls     [][]httpx.ImportDocument
	banks     []string
}

func (v *importGateValidator) ValidateOperation(context.Context, httpx.OperationRequest) httpx.OperationDecision {
	return httpx.OperationDecision{Allowed: true}
}

func (v *importGateValidator) ResolveTagScope(context.Context, string) []httpx.TagGroup { return nil }

func (v *importGateValidator) ValidateImport(_ context.Context, bankID string, documents []httpx.ImportDocument) httpx.OperationDecision {
	v.calls = append(v.calls, documents)
	v.banks = append(v.banks, bankID)
	for _, document := range documents {
		if document.Content == v.overQuota {
			return httpx.OperationDecision{Allowed: false, Reason: "over quota", Status: v.status}
		}
	}
	return httpx.OperationDecision{Allowed: true}
}

// gateArchive builds a valid archive holding one document per (id, text) pair.
func gateArchive(t *testing.T, sourceBank string, ids, texts []string) []byte {
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
	entry, err := zw.Create(transferarchive.ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(manifestJSON); err != nil {
		t.Fatal(err)
	}
	for index, id := range ids {
		payload, err := json.Marshal(transferarchive.Document{ID: id, OriginalText: texts[index]})
		if err != nil {
			t.Fatal(err)
		}
		name := transferarchive.DocumentsDir + "/00000" + string(rune('0'+index)) + ".json"
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// gateMultipart wraps an archive in the multipart body the import routes take.
func gateMultipart(t *testing.T, data []byte) (*bytes.Buffer, string) {
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

// A deployment's import gate refuses an archive at submission on both doors it
// guards — the document import and the whole-bank restore — and a refusal
// creates nothing: no bank, no stored archive, no queued operation. This is the
// reference's validate_import hook (engine/memory_engine.py:8244-8252,
// :9076-9079, :9175-9196), pinned end to end by
// hindsight-system-tests/tests/test_84_import_gate.py.
func TestAppImportGateRefusesAnArchiveOnBothDoors(t *testing.T) {
	application, err := New(context.Background(), testConfig(),
		Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	validator := &importGateValidator{overQuota: "OVER-QUOTA: Alice moved to Berlin.", status: http.StatusPaymentRequired}
	// Registering after composition is the ordinary path: the transfer family
	// asks the host for its gate per submission, so a validator registered after
	// New still guards every import.
	application.Extensions.RegisterOperationValidator(validator)

	if _, err := application.Store.CreateBank(context.Background(), storage.Bank{BankID: "source", Name: "source"}); err != nil {
		t.Fatal(err)
	}
	ordinary := gateArchive(t, "source", []string{"d1"}, []string{"Alice moved to Berlin."})
	heavy := gateArchive(t, "source", []string{"d2"}, []string{"OVER-QUOTA: Alice moved to Berlin."})

	// The document import door: accepted while the archive passes the gate.
	body, contentType := gateMultipart(t, ordinary)
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/default/banks/neighbour/document-transfer", body)
	request.Header.Set("Content-Type", contentType)
	application.Handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("an ordinary archive: status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}

	// Refused, with the validator's own status and reason, and nothing created.
	body, contentType = gateMultipart(t, heavy)
	rec = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/default/banks/refused/document-transfer", body)
	request.Header.Set("Content-Type", contentType)
	application.Handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("an over-quota archive: status = %d, want 402 (%s)", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"detail":"over quota"`)) {
		t.Fatalf("refusal body = %s, want the validator's reason", rec.Body.String())
	}
	if _, err := application.Store.GetBank(context.Background(), "refused"); err == nil {
		t.Fatal("a refused document import must not create the bank")
	}
	queued, err := application.Store.ListOperations(context.Background(), storage.OperationQuery{BankID: "refused"})
	if err != nil {
		t.Fatal(err)
	}
	if len(queued.Operations) != 0 {
		t.Fatalf("a refused document import must queue nothing, got %d", len(queued.Operations))
	}

	// The whole-bank restore door: the same gate, against the target bank.
	body, contentType = gateMultipart(t, heavy)
	rec = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost,
		"/v1/default/banks/source/transfer/import?target_bank_id=copy", body)
	request.Header.Set("Content-Type", contentType)
	application.Handler.ServeHTTP(rec, request)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("a refused restore: status = %d, want 402 (%s)", rec.Code, rec.Body.String())
	}
	if _, err := application.Store.GetBank(context.Background(), "copy"); err == nil {
		t.Fatal("a refused restore must not create the target bank")
	}
	restoreQueued, err := application.Store.ListOperations(context.Background(), storage.OperationQuery{BankID: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if len(restoreQueued.Operations) != 0 {
		t.Fatalf("a refused restore must queue nothing, got %d", len(restoreQueued.Operations))
	}

	// The gate saw every archive's documents with their ids and text, and the
	// bank each archive lands in.
	if len(validator.calls) != 3 {
		t.Fatalf("gate calls = %d, want 3 (allowed import, refused import, refused restore)", len(validator.calls))
	}
	if got := validator.calls[1]; len(got) != 1 || got[0].DocumentID != "d2" ||
		got[0].Content != "OVER-QUOTA: Alice moved to Berlin." {
		t.Fatalf("gate documents = %v, want the archive's own document", got)
	}
	if validator.banks[1] != "refused" || validator.banks[2] != "copy" {
		t.Fatalf("gate banks = %v, want the landing bank on each door", validator.banks)
	}
}

// With no validator registered the two import doors answer exactly as they do
// with a validator that carries no import rule: the unextended composition is
// byte-identical.
func TestAppNoImportGateResponseUnchanged(t *testing.T) {
	plain, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	wired, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	wired.Extensions.RegisterOperationValidator(appValidator{})

	archive := gateArchive(t, "source", []string{"d1"}, []string{"Alice moved to Berlin."})
	for _, target := range []string{
		"/v1/default/banks/fresh/document-transfer",
		"/v1/default/banks/source/transfer/import?target_bank_id=copy",
	} {
		first := httptest.NewRecorder()
		body, contentType := gateMultipart(t, archive)
		request := httptest.NewRequest(http.MethodPost, target, body)
		request.Header.Set("Content-Type", contentType)
		plain.Handler.ServeHTTP(first, request)

		second := httptest.NewRecorder()
		body, contentType = gateMultipart(t, archive)
		request = httptest.NewRequest(http.MethodPost, target, body)
		request.Header.Set("Content-Type", contentType)
		wired.Handler.ServeHTTP(second, request)

		if first.Code != second.Code {
			t.Fatalf("%s: status = %d with no validator, %d with one carrying no import rule",
				target, first.Code, second.Code)
		}
		if first.Code == http.StatusAccepted {
			// The operation id is fresh per submission; the accepted status
			// sentence must still match.
			if !bytes.Contains(second.Body.Bytes(), []byte(`"status":"pending"`)) {
				t.Fatalf("%s: accepted body = %s", target, second.Body.String())
			}
			continue
		}
		if first.Body.String() != second.Body.String() {
			t.Fatalf("%s: body = %s vs %s", target, first.Body.String(), second.Body.String())
		}
	}
}
