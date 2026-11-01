package transfer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// offeringStore is a store that offers a time-limited direct URL, which is the
// capability an object-store backend adds. It embeds the in-memory store so
// every other method is unchanged. It is declared here so the download route is
// exercised against a backend that offers one, not only against one that cannot:
// the route must answer the same either way.
type offeringStore struct {
	*storage.MemoryStore
	url string
}

// StoredFileDownloadURL reports the direct URL the backend could hand out. No
// interface in this service declares it any more — the route streams the object
// itself — so this method exists only to prove an offering is ignored.
func (s offeringStore) StoredFileDownloadURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return s.url, nil
}

// t-download-streams-not-redirects: a store that offers a direct URL serves the
// archive through the route anyway. The download contract is a 200 ZIP response
// with the attachment filename and, when the stored object reports its size,
// the content length — the signed URL is not a response the route hands out.
func TestDownloadStreamsRatherThanRedirectingSignedURL(t *testing.T) {
	store := storage.NewMemoryStore("public")
	direct := "https://objects.example/banks/bank-a/exports/archive.zip?X-Amz-Signature=abc"
	h := NewHandlers(offeringStore{MemoryStore: store, url: direct}, true, true)
	seedBank(t, store, "bank-a")

	key := storage.StoredFileKeyForBank("bank-a", "archive.zip")
	if err := store.PutStoredFile(context.Background(), storage.StoredFile{
		Key: key, ContentType: "application/zip", Data: []byte("zip-bytes"),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/default/files/download/"+key, nil)
	h.ServeDownload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("location = %q, want no redirect", got)
	}
	if rec.Body.String() != "zip-bytes" {
		t.Fatalf("body = %q, want the archive bytes", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content-type = %q, want application/zip", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="bank-a-documents.zip"` {
		t.Fatalf("disposition = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "9" {
		t.Fatalf("content-length = %q, want 9", got)
	}
}

// A store that offers no direct URL — an anonymous bucket — serves the same 200
// body, so every backend answers a download identically.
func TestDownloadStreamsWithoutAnOfferedURL(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(offeringStore{MemoryStore: store, url: ""}, true, true)
	seedBank(t, store, "bank-a")

	key := storage.StoredFileKeyForBank("bank-a", "archive.zip")
	if err := store.PutStoredFile(context.Background(), storage.StoredFile{
		Key: key, ContentType: "application/zip", Data: []byte("zip-bytes"),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/default/files/download/"+key, nil)
	h.ServeDownload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "zip-bytes" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
