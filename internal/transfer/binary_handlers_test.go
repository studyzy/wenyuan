package transfer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seedAttachment stores an attachment row and its bytes at the key the row
// names, which is what the attachment route resolves.
func seedAttachment(t *testing.T, store *storage.MemoryStore, bankID, shortID, documentID string, mediaType string, data []byte) {
	t.Helper()
	key := storage.AttachmentStorageKey(bankID, documentID, "hash-"+shortID)
	if err := store.PutAttachment(context.Background(), storage.Attachment{
		BankID: bankID, DocumentID: documentID, AttachmentHash: "hash-" + shortID, ShortID: shortID,
		MediaType: mediaType, ByteSize: int64(len(data)), Kind: "image", StorageKey: key,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutStoredFile(context.Background(), storage.StoredFile{
		Key: key, ContentType: mediaType, Data: data,
	}); err != nil {
		t.Fatal(err)
	}
}

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
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="bank-a-documents.zip"` {
		t.Fatalf("disposition = %q", got)
	}
	// The download route serves exactly what the reference serves: no
	// no-sniff directive and no cache directive.
	if got := rec.Header().Get("X-Content-Type-Options"); got != "" {
		t.Fatalf("no-sniff directive = %q, want none", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("cache directive = %q, want none", got)
	}
	if rec.Body.String() != "zip-bytes" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

// test-download-key-is-used-verbatim: the reference splits the raw path
// remainder, so a key padded with a leading or doubled slash leaves an empty
// first segment and never resolves — even when the trimmed key would name a
// stored file.
func TestDownloadRejectsSlashPaddedKey(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	if err := store.PutStoredFile(context.Background(), storage.StoredFile{
		Key: "banks/bank-a/x.zip", ContentType: "application/zip", Data: []byte("zip"),
	}); err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{
		"/v1/default/files/download//banks/bank-a/x.zip",
		"/v1/default/files/download/banks/bank-a/x.zip/",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		h.ServeDownload(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404 (body %s)", raw, rec.Code, rec.Body.String())
		}
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

// test-download-is-gated: a disabled document-export gate answers the download
// route with the reference's disabled message before any key work.
func TestDownloadIsGated(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(store, false, true)
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
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Body.String(); !strings.Contains(got, "HINDSIGHT_API_ENABLE_DOCUMENT_EXPORT_API") {
		t.Fatalf("disabled detail must name the flag: %s", got)
	}
}

// test-download-accepts-tenant-prefixed-keys: the reference stores keys under
// both the tenants/{schema}/banks/... and banks/... layouts, and both resolve.
func TestDownloadAcceptsTenantPrefixedKeys(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	key := "tenants/public/banks/bank-a/exports/op/transfer.zip"
	if err := store.PutStoredFile(context.Background(), storage.StoredFile{
		Key: key, ContentType: "application/zip", Data: []byte("tenant-zip"),
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/default/files/download/"+key, nil)
	h.ServeDownload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "tenant-zip" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="bank-a-documents.zip"` {
		t.Fatalf("disposition = %q", got)
	}
}

// test-attachment-not-found-names-the-resource: the reference's attachment
// miss reports "Attachment not found", not a generic message.
func TestAttachmentNotFoundNamesTheResource(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	rec := httptest.NewRecorder()
	h.ServeAttachment(rec, routeRequest(http.MethodGet, "/v1/default/banks/bank-a/attachments/nope", nil,
		map[string]string{"bank_id": "bank-a", "attachment_id": "nope"}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Body.String(); got != `{"detail":"Attachment not found"}` {
		t.Fatalf("body = %s", got)
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

	seedAttachment(t, store, "bank-a", "shot", "doc-1", "image/png", []byte("png-bytes"))

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodGet, "/v1/default/banks/bank-a/attachments/shot", nil,
		map[string]string{"bank_id": "bank-a", "attachment_id": "shot"})
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

// test-http-error-envelope-carries-only-detail: a missing-bank response carries
// the reference's message, which is the only thing in the body.
func TestErrorEnvelopeCarriesOnlyDetail(t *testing.T) {
	h, _ := newTestHandlers(t)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/missing/transfer/export", nil,
		map[string]string{"bank_id": "missing"})
	h.SubmitTransferExport(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Body.String(); got != `{"detail":"Bank 'missing' not found"}` {
		t.Fatalf("envelope = %s", got)
	}
}

// bufferedOnlyStore hides the streaming capability of the store it wraps, so
// a handler falls back to the buffered read exactly as it did before the
// capability existed. Embedding the Store interface surfaces only the methods
// store.go declares, so the wrapper does not satisfy StoredFileStreamer.
type bufferedOnlyStore struct {
	storage.Store
}

// headerSnapshot copies every response header so two responses can be compared
// field by field.
func headerSnapshot(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for name, values := range h {
		out[name] = strings.Join(values, ",")
	}
	return out
}

// test-streamed-serve-matches-buffered: the streaming path returns the same
// bytes and headers as the buffered path, for both byte-serving routes. The
// store implementing the capability and the wrapper hiding it are served the
// same payload, so any difference in the response is the streaming change.
func TestStreamedServeMatchesBuffered(t *testing.T) {
	streaming, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	buffered := NewHandlers(bufferedOnlyStore{Store: store}, true, true)

	// The attachment route resolves an attachment row, so its bytes live at the
	// key that row names; the download route addresses a key directly.
	seedAttachment(t, store, "bank-a", "shot", "doc-1", "image/png", []byte("png-bytes"))
	downloadKey := storage.StoredFileKeyForBank("bank-a", "archive.zip")
	for _, f := range []storage.StoredFile{
		{Key: downloadKey, ContentType: "application/zip", Data: []byte("zip-payload")},
	} {
		if err := store.PutStoredFile(context.Background(), f); err != nil {
			t.Fatal(err)
		}
	}

	serve := func(h *Handlers, route, attachment string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var req *http.Request
		if route == "attachment" {
			req = routeRequest(http.MethodGet, "/v1/default/banks/bank-a/attachments/"+attachment, nil,
				map[string]string{"bank_id": "bank-a", "attachment_id": attachment})
			h.ServeAttachment(rec, req)
		} else {
			req = httptest.NewRequest(http.MethodGet, "/v1/default/files/download/"+downloadKey, nil)
			h.ServeDownload(rec, req)
		}
		return rec
	}

	for _, tc := range []struct {
		name       string
		route      string
		attachment string
		body       string
	}{
		{"attachment", "attachment", "shot", "png-bytes"},
		{"download", "download", "", "zip-payload"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := serve(buffered, tc.route, tc.attachment)
			got := serve(streaming, tc.route, tc.attachment)
			if got.Code != want.Code {
				t.Fatalf("status = %d, want %d", got.Code, want.Code)
			}
			if got.Body.String() != want.Body.String() {
				t.Fatalf("body = %q, want %q", got.Body.String(), want.Body.String())
			}
			if got.Body.String() != tc.body {
				t.Fatalf("body = %q, want %q", got.Body.String(), tc.body)
			}
			wantHeaders := headerSnapshot(want.Header())
			gotHeaders := headerSnapshot(got.Header())
			if len(wantHeaders) != len(gotHeaders) {
				t.Fatalf("headers = %v, want %v", gotHeaders, wantHeaders)
			}
			for name, value := range wantHeaders {
				if gotHeaders[name] != value {
					t.Fatalf("header %q = %q, want %q", name, gotHeaders[name], value)
				}
			}
			if got.Header().Get("Content-Length") == "" {
				t.Fatal("a streamed response with a known size must declare Content-Length")
			}
		})
	}
}

// test-backend-without-streaming-still-serves: a store that does not implement
// the streaming capability keeps serving both routes through the buffered
// path, so the capability is optional rather than required.
func TestBackendWithoutStreamingStillServes(t *testing.T) {
	_, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")
	h := NewHandlers(bufferedOnlyStore{Store: store}, true, true)

	if _, ok := h.Store.(storage.StoredFileStreamer); ok {
		t.Fatal("the wrapper must not expose the streaming capability")
	}

	seedAttachment(t, store, "bank-a", "shot", "doc-1", "image/png", []byte("png-bytes"))

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodGet, "/v1/default/banks/bank-a/attachments/shot", nil,
		map[string]string{"bank_id": "bank-a", "attachment_id": "shot"})
	h.ServeAttachment(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "png-bytes" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Content-Length") != itoa(len("png-bytes")) {
		t.Fatalf("content length = %q", rec.Header().Get("Content-Length"))
	}
}

// test-streamed-missing-file-is-not-found: the streaming path reports a miss
// the same way the buffered path does, so the route stays non-probing.
func TestStreamedMissingFileIsNotFound(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "bank-a")

	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodGet, "/v1/default/banks/bank-a/attachments/nope", nil,
		map[string]string{"bank_id": "bank-a", "attachment_id": "nope"})
	h.ServeAttachment(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// A retained attachment is served by its short id: the row names the storage
// key, and guessing the key from the id never matched a retained attachment.
func TestAttachmentIsServedByItsShortID(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAttachment(ctx, storage.Attachment{
		BankID: "b1", DocumentID: "doc-1", AttachmentHash: "hash-1", ShortID: "att-1",
		MediaType: "image/png", ByteSize: 4, Kind: "image", Filename: "shot.png",
		StorageKey: storage.AttachmentStorageKey("b1", "doc-1", "hash-1"),
	}); err != nil {
		t.Fatal(err)
	}
	// The bytes live at the key the row names, with a different media type than
	// the attachment declared — the declaration must win.
	if err := store.PutStoredFile(ctx, storage.StoredFile{
		Key:         storage.AttachmentStorageKey("b1", "doc-1", "hash-1"),
		ContentType: "application/octet-stream", Data: []byte("PNG!"),
	}); err != nil {
		t.Fatal(err)
	}

	h := NewHandlers(store, true, true)
	rec := httptest.NewRecorder()
	h.ServeAttachment(rec, routeRequest(http.MethodGet, "/v1/default/banks/b1/attachments/att-1", nil,
		map[string]string{"bank_id": "b1", "attachment_id": "att-1"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the retained attachment's bytes: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "PNG!" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("content type = %q, want the declared media type", got)
	}
	// An unknown id in a known bank is not found, indistinguishably from an
	// unknown bank.
	missing := httptest.NewRecorder()
	h.ServeAttachment(missing, routeRequest(http.MethodGet, "/v1/default/banks/b1/attachments/nope", nil,
		map[string]string{"bank_id": "b1", "attachment_id": "nope"}))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("unknown attachment = %d", missing.Code)
	}
}
