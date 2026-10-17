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
	if err := writer.WriteField("request", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, writer.FormDataContentType()
}

// A single upload larger than the configured retention-attachment size is
// accepted: the reference places no per-file cap on the /files/retain route,
// bounding only the whole batch (the conversion batch size and byte total).
func TestUploadHasNoPerFileSizeCap(t *testing.T) {
	mux, store := limitedServer(t, 16, 5)
	req, _ := uploadFiles(t, map[string]string{"big.txt": strings.Repeat("x", 64)})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	documents, err := store.ListDocuments(context.TODO(), storage.DocumentQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if documents.Total != 1 {
		t.Fatalf("the upload must be stored: %+v", documents.Documents)
	}
}

// The file route's count bound is the conversion batch size
// (HINDSIGHT_API_FILE_CONVERSION_MAX_BATCH_SIZE), not the retention attachment
// count: a request above the latter but within the former is accepted.
func TestUploadCountBoundIsConversionBatchSize(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.MaxAttachments = 1
	h.MaxFileBatchCount = 2
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	req, _ := uploadFiles(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a request within the conversion batch size must be accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// More files than the conversion batch size is refused, naming the bound.
func TestUploadOverBatchSizeRefused(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.MaxFileBatchCount = 1
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	req, _ := uploadFiles(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Maximum 1 files per request") {
		t.Fatalf("the refusal must name the count: %s", rec.Body.String())
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
