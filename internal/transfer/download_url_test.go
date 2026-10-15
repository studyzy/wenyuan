package transfer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// signingStore is a store that offers a time-limited direct URL, which is the
// capability an object-store backend adds. It embeds the in-memory store so
// every other method is unchanged.
type signingStore struct {
	*storage.MemoryStore
	url string
}

// StoredFileDownloadURL reports the direct URL the download route should hand
// out.
func (s signingStore) StoredFileDownloadURL(_ context.Context, _ string, _ time.Duration) (string, error) {
	return s.url, nil
}

// A store that can sign a URL hands the caller a redirect to it rather than
// streaming the archive through the API.
func TestDownloadRedirectsToSignedURL(t *testing.T) {
	store := storage.NewMemoryStore("public")
	direct := "https://objects.example/banks/bank-a/exports/archive.zip?X-Amz-Signature=abc"
	h := NewHandlers(signingStore{MemoryStore: store, url: direct}, true, true)
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

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusFound, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != direct {
		t.Fatalf("location = %q, want %q", got, direct)
	}
}

// A store that cannot sign — an anonymous bucket, or the row backend — keeps
// serving the payload through the route, so the redirect never becomes the
// only path.
func TestDownloadFallsBackToProxyingWithoutSignedURL(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(signingStore{MemoryStore: store, url: ""}, true, true)
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
