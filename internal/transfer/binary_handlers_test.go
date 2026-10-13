package transfer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// test-download-rejects-traversal: a key containing a parent-directory segment
// answers not-found and no read occurs.
func TestDownloadRejectsParentDirectorySegment(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	keys := []string{
		"../../../etc/passwd",
		"banks/bank-a/../../secret.zip",
		"banks/../bank-a/file.zip",
		"banks/bank-a/./file.zip",
	}
	for _, key := range keys {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/default/files/download/"+key, nil)
		h.ServeDownload(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("key %q: status = %d, want %d", key, rec.Code, http.StatusNotFound)
		}
	}
}

// test-download-not-bank-scoped: the download route resolves the owning bank
// from the key itself, because it has no bank path segment.
func TestDownloadResolvesOwningBankFromKey(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	key := storage.StoredFileKeyForBank("bank-a", "archive.zip")
	if err := store.PutStoredFile(context.Background(), storage.StoredFile{
		Key:         key,
		ContentType: "application/zip",
		Data:        []byte("zip-bytes"),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/default/files/download/"+key, nil)
	h.ServeDownload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("content type = %q, want application/zip", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got == "" {
		t.Fatal("download must declare an attachment disposition")
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("no-sniff directive = %q", got)
	}
	if rec.Body.String() != "zip-bytes" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// test-download-key-outside-bank-prefix: a key that is not in the bank-scoped
// layout resolves to nothing.
func TestDownloadKeyOutsideBankPrefixIsNotFound(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	if err := store.PutStoredFile(context.Background(), storage.StoredFile{
		Key:  "loose/key.zip",
		Data: []byte("x"),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/default/files/download/loose/key.zip", nil)
	h.ServeDownload(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// test-attachment-and-invisible-bank-share-one-answer: a missing attachment
// and a bank the store does not expose both answer not-found with the same
// body, so the route cannot probe a bank's holdings.
func TestAttachmentNotFoundIsIndistinguishable(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	missingInKnownBank := httptest.NewRecorder()
	req := routeRequest(http.MethodGet, "/v1/default/banks/bank-a/attachments/nope", nil,
		map[string]string{"bank_id": "bank-a", "attachment_id": "nope"})
	h.ServeAttachment(missingInKnownBank, req)

	unknownBank := httptest.NewRecorder()
	req2 := routeRequest(http.MethodGet, "/v1/default/banks/ghost/attachments/nope", nil,
		map[string]string{"bank_id": "ghost", "attachment_id": "nope"})
	h.ServeAttachment(unknownBank, req2)

	if missingInKnownBank.Code != http.StatusNotFound || unknownBank.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d / %d, want both %d",
			missingInKnownBank.Code, unknownBank.Code, http.StatusNotFound)
	}
	if missingInKnownBank.Body.String() != unknownBank.Body.String() {
		t.Fatalf("bodies must be indistinguishable: %q vs %q",
			missingInKnownBank.Body.String(), unknownBank.Body.String())
	}
}

// test-attachment-sets-nosniff-and-cache: the attachment read declares the
// media type, the no-sniff directive, and a private immutable cache directive.
func TestAttachmentHeaders(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	key := storage.StoredFileKeyForBank("bank-a", "shot.png")
	if err := store.PutStoredFile(context.Background(), storage.StoredFile{
		Key:         key,
		ContentType: "image/png",
		Data:        []byte("png-bytes"),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodGet, "/v1/default/banks/bank-a/attachments/shot.png", nil,
		map[string]string{"bank_id": "bank-a", "attachment_id": "shot.png"})
	h.ServeAttachment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("no-sniff = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("cache directive = %q", got)
	}
}

// test-key-builder-encodes-segments: a hostile identifier cannot nest under
// another prefix or introduce a traversal segment at write time.
func TestKeyBuilderEncodesSegments(t *testing.T) {
	key := storage.StoredFileKeyForBank("../other", "a/b.zip")
	if err := storage.ValidateStoredFileKey(key); err != nil {
		t.Fatalf("built key must be valid: %v (key %q)", err, key)
	}
	bankID, ok := storage.BankIDFromStoredFileKey(key)
	if !ok {
		t.Fatalf("built key must resolve an owning bank: %q", key)
	}
	if bankID != "../other" {
		t.Fatalf("recovered bank = %q, want the original identifier", bankID)
	}
}

// test-http-error-envelope-carries-only-detail: the transfer handlers never
// leak an internal identifier in a response body.
func TestErrorEnvelopeCarriesOnlyDetail(t *testing.T) {
	h, _ := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/missing/transfer/export", nil,
		map[string]string{"bank_id": "missing"})
	h.SubmitTransferExport(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Body.String(); got != `{"detail":"bank not found"}` {
		t.Fatalf("envelope = %s", got)
	}
}
