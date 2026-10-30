package memoryops

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/fileparse"
	"github.com/studyzy/wenyuan/internal/storage"
)

// configuredServer builds a served handler set, lets the caller adjust the
// file-conversion settings, and returns the mux and store.
func configuredServer(t *testing.T, configure func(*Handlers)) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	if configure != nil {
		configure(h)
	}
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux, store
}

// decliningParser is a parser that reports the file unsupported, exercising the
// chain's fallthrough at the route.
type decliningParser struct{ name string }

func (p decliningParser) Name() string               { return p.name }
func (decliningParser) Supports(string, string) bool { return true }

func (p decliningParser) Convert([]byte, string) (string, error) {
	return "", &fileparse.UnsupportedFileTypeError{Parser: p.name, Filename: "f", Reason: "declines"}
}

// fixedParser returns a scripted markdown body.
type fixedParser struct {
	name    string
	content string
}

func (p fixedParser) Name() string                           { return p.name }
func (fixedParser) Supports(string, string) bool             { return true }
func (p fixedParser) Convert([]byte, string) (string, error) { return p.content, nil }

// A per-file parser chain whose first member declines falls through to the
// second, and the second's markdown reaches the store.
func TestFileRetainChainFallsThroughAtRoute(t *testing.T) {
	registry := fileparse.NewRegistry()
	registry.Register(decliningParser{name: "first"})
	registry.Register(fixedParser{name: "second", content: "# converted by second"})
	mux, store := configuredServer(t, func(h *Handlers) {
		h.Parsers = registry
		h.ParserChain = []string{"first", "second"}
	})
	rec := uploadFile(t, mux, "notes.txt", "raw bytes", "text/plain")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	drainQueuedFileRetains(t, store)
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, unit := range page.Memories {
		if strings.Contains(unit.Text, "converted by second") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the second parser's output did not reach the store: %+v", page.Memories)
	}
}

// A parser the allowlist does not offer is rejected before any file is read.
func TestFileRetainAllowlistRejectsUnavailableParser(t *testing.T) {
	mux, _ := configuredServer(t, func(h *Handlers) {
		h.ParserAllowlist = []string{"markitdown"}
	})
	rec := uploadWithRequest(t, mux, "notes.txt", "body", `{"parser":"iris"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("an unlisted parser must be rejected, body = %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "iris") {
		t.Fatalf("the refusal must name the parser: %s", rec.Body.String())
	}
}

// More files than the configured batch size is refused with the reference's
// message.
func TestFileRetainBatchCountLimit(t *testing.T) {
	mux, _ := configuredServer(t, func(h *Handlers) { h.MaxFileBatchCount = 1 })
	req, _ := uploadFiles(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Too many files. Maximum 1 files per request.") {
		t.Fatalf("the refusal must name the batch limit: %s", rec.Body.String())
	}
}

// A combined upload larger than the configured batch size is refused.
func TestFileRetainBatchBytesLimit(t *testing.T) {
	mux, _ := configuredServer(t, func(h *Handlers) { h.MaxFileBatchBytes = 4 })
	rec := uploadFile(t, mux, "big.txt", strings.Repeat("x", 64), "text/plain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Total batch size") {
		t.Fatalf("the refusal must name the batch size: %s", rec.Body.String())
	}
}

// The delete-after-retain and keep-bytes behaviour is asserted end to end in
// `internal/app`, because the file route always processes asynchronously: the
// attachment row and the stored bytes are written by the queued operation, not
// by the request. A unit test here can only observe the enqueue.

// An empty parser name is a name, not "no parser requested": the reference
// resolves it to a one-element chain that fails the allowlist, and so does this.
func TestFileRetainEmptyStringParserRejected(t *testing.T) {
	for _, body := range []string{`{"parser":""}`, `{"files_metadata":[{"parser":""}]}`} {
		mux, _ := configuredServer(t, nil)
		rec := uploadWithRequest(t, mux, "notes.txt", "body", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400, body = %s", body, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "['']") {
			t.Fatalf("%s: the refusal must name the empty parser: %s", body, rec.Body.String())
		}
	}
}

// An empty files_metadata list is absent, not a zero-length alignment: the
// reference's truthiness test skips the count check, so the upload succeeds.
func TestFileRetainEmptyMetadataListNotAMismatch(t *testing.T) {
	mux, _ := configuredServer(t, nil)
	rec := uploadWithRequest(t, mux, "notes.txt", "body", `{"files_metadata":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
}

// The reference's request gates run in a fixed order: the file count is checked
// before the files_metadata alignment and the request-level parser, so a request
// that trips several names the count.
func TestFileRetainCountGatePrecedesOtherGates(t *testing.T) {
	cases := []string{
		`{"files_metadata":[{"tags":["x"]}]}`,
		`{"parser":"nope"}`,
	}
	for _, request := range cases {
		mux, _ := configuredServer(t, func(h *Handlers) { h.MaxFileBatchCount = 1 })
		req := uploadRequestWith(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"}, request)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400, body = %s", request, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Too many files") {
			t.Fatalf("%s: the count gate must run first: %s", request, rec.Body.String())
		}
	}
}

// uploadRequestWith builds one multipart request carrying several files and the
// given `request` form field.
func uploadRequestWith(t *testing.T, files map[string]string, request string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.WriteField("request", request); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

// A bad per-file parser is named before the batch byte bound, which the
// reference checks only after it has validated every file's chain.
func TestFileRetainParserGatePrecedesBatchBytes(t *testing.T) {
	mux, _ := configuredServer(t, func(h *Handlers) { h.MaxFileBatchBytes = 4 })
	rec := uploadWithRequest(t, mux, "notes.txt", strings.Repeat("x", 64), `{"files_metadata":[{"parser":"nope"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "nope") {
		t.Fatalf("the parser gate must run before the byte gate: %s", rec.Body.String())
	}
}

// A multipart body with neither part reports both, in declaration order; one
// with only the request reports just the files part.
func TestFileRetainMissingPartsReportedInOrder(t *testing.T) {
	cases := []struct {
		name    string
		parts   []string
		missing []string
	}{
		{"neither", nil, []string{"files", "request"}},
		{"files only", []string{"files"}, []string{"request"}},
	}
	for _, tc := range cases {
		mux, _ := configuredServer(t, nil)
		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		for _, name := range tc.parts {
			part, err := writer.CreateFormFile(name, "a.txt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("body")); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 422, body = %s", tc.name, rec.Code, rec.Body.String())
		}
		for _, want := range tc.missing {
			if !strings.Contains(rec.Body.String(), `"`+want+`"`) {
				t.Fatalf("%s: the refusal must name %s: %s", tc.name, want, rec.Body.String())
			}
		}
		if tc.name == "neither" {
			body := rec.Body.String()
			if strings.Index(body, `"files"`) > strings.Index(body, `"request"`) {
				t.Fatalf("missing parts must be declared in order: %s", body)
			}
		}
	}
}

// uploadWithRequest posts one file with an optional `request` form field.
func uploadWithRequest(t *testing.T, mux *http.ServeMux, filename, body, request string) *httptest.ResponseRecorder {
	t.Helper()
	var buf strings.Builder
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("files", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if request == "" {
		request = "{}"
	}
	if err := writer.WriteField("request", request); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// A converted upload stores its text as the document's content and no attachment
// row: verified against the live reference, where a text upload's document reads
// `attachments: null` and no row is written to its attachments table. Only an
// image keeps its bytes as an attachment.
func TestFileRetainConvertedUploadKeepsNoAttachment(t *testing.T) {
	mux, store := configuredServer(t, nil)
	rec := uploadWithRequest(t, mux, "notes.txt", "Alice likes tea.",
		`{"async":false,"files_metadata":[{"document_id":"converted-doc"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	drainQueuedFileRetains(t, store)
	// The upload names its document through files_metadata, so the id the route
	// was told to use is the one to read back; the async retain creates it.
	document, err := store.GetDocument(context.TODO(), "b1", "converted-doc")
	if err != nil {
		t.Fatalf("the converted upload stored no document: %v", err)
	}
	if document.OriginalText != "Alice likes tea." {
		t.Fatalf("document text = %q, want the converted body", document.OriginalText)
	}
	attachments, err := store.ListAttachments(context.TODO(), "b1", document.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 0 {
		t.Fatalf("a converted upload kept %d attachment row(s), want none: %+v", len(attachments), attachments)
	}
}
