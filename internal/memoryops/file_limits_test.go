package memoryops

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// limitedServer builds a served handler set with the upload caps configured.
func limitedServer(t *testing.T, maxBytes int64, maxAttachments int) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.MaxAttachmentBytes = maxBytes
	h.MaxAttachments = maxAttachments
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux, store
}

// uploadFiles builds one multipart request carrying the named files.
func uploadFiles(t *testing.T, files map[string]string) (*http.Request, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, content := range files {
		part, err := writer.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, writer.FormDataContentType()
}

// A file larger than the configured limit is refused before it is stored, and
// the refusal names the limit.
func TestUploadOverSizeLimitRefused(t *testing.T) {
	mux, store := limitedServer(t, 16, 5)
	req, _ := uploadFiles(t, map[string]string{"big.txt": strings.Repeat("x", 64)})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "exceeding") {
		t.Fatalf("the refusal must name the limit: %s", rec.Body.String())
	}
	// The refusal happens before any bytes are stored, so no document exists.
	documents, err := store.ListDocuments(context.TODO(), storage.DocumentQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if documents.Total != 0 {
		t.Fatalf("an oversize upload must not be stored: %+v", documents.Documents)
	}
}

// More files than the configured count is refused, and a file within the limits
// is accepted.
func TestUploadOverCountLimitRefused(t *testing.T) {
	mux, _ := limitedServer(t, 1<<20, 1)
	req, _ := uploadFiles(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "1-file limit") {
		t.Fatalf("the refusal must name the count: %s", rec.Body.String())
	}

	ok, _ := limitedServer(t, 1<<20, 2)
	req, _ = uploadFiles(t, map[string]string{"a.txt": "alpha"})
	rec = httptest.NewRecorder()
	ok.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a file within the limits must be accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// The body bound admits the configured files at the configured size, and an
// unconfigured deployment leaves the body unbounded.
func TestUploadBodyLimit(t *testing.T) {
	configured := &Handlers{MaxAttachmentBytes: 1 << 20, MaxAttachments: 2}
	if bound := configured.uploadBodyLimit(); bound <= 2<<20 {
		t.Fatalf("body bound = %d, want room for the configured files", bound)
	}
	if bound := (&Handlers{}).uploadBodyLimit(); bound != 0 {
		t.Fatalf("an unconfigured deployment leaves the body unbounded: %d", bound)
	}
}
