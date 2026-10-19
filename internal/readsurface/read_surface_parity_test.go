package readsurface

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The chunk routes keep the attachments key with a null value when a chunk's
// text references none: the reference's chunk model always carries the key, so
// dropping it makes the payload a different shape on this backend.
func TestChunkRoutesCarryNullAttachments(t *testing.T) {
	mux := serve(t, seededStore(t))
	base := "/v1/default/banks/" + bank + "/documents/doc-1"

	rec := send(mux, http.MethodGet, base+"/chunks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("chunks: %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"attachments":null`) {
		t.Fatalf("chunk listing must carry a null attachments key: %s", rec.Body.String())
	}

	var listing ChunkListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Items) == 0 {
		t.Fatal("the fixture document has no chunks")
	}
	// The identifier route carries the same key, including when the chunk's bank
	// and document are resolved from the chunk itself.
	chunkRec := send(mux, http.MethodGet, "/v1/default/chunks/"+listing.Items[0].ChunkID, "")
	if chunkRec.Code != http.StatusOK {
		t.Fatalf("chunk detail: %d, body = %s", chunkRec.Code, chunkRec.Body.String())
	}
	if !strings.Contains(chunkRec.Body.String(), `"attachments":null`) {
		t.Fatalf("chunk detail must carry a null attachments key: %s", chunkRec.Body.String())
	}
}

// An attachment the caller named nothing keeps filename as null rather than
// dropping the key, the shape the reference renders for it.
func TestChunkAttachmentKeepsNullFilename(t *testing.T) {
	encoded, err := json.Marshal(chunkAttachment(bank, storage.Attachment{ShortID: "abc123", AttachmentHash: "abc123" + strings.Repeat("0", 58)}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"filename":null`) {
		t.Fatalf("attachment without a name = %s, want a null filename key", encoded)
	}
}

// A listing's string filters are handed to the store exactly as the caller
// wrote them, the way the reference's query parameters are: a padded search
// term does not become an unqualified one, and an empty tag (`?tags=`) is a tag
// to filter on rather than an absent filter. Both turn a no-match into a match
// on this side if they are normalised away.
func TestListingStringFiltersAreVerbatim(t *testing.T) {
	mux := serve(t, seededStore(t))
	base := "/v1/default/banks/" + bank

	cases := []struct {
		path  string
		total int
	}{
		{base + "/documents", 1},
		{base + "/documents?q=doc", 1},
		{base + "/documents?q=%20doc%20", 0},
		{base + "/documents?tags=", 0},
		{base + "/documents?tags=work", 1},
		{base + "/entities?tags=", 0},
		{base + "/graph", 1},
		{base + "/graph?q=%20Alice%20", 0},
		{base + "/graph?q=Alice", 1},
	}
	for _, tc := range cases {
		rec := send(mux, http.MethodGet, tc.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d, body = %s", tc.path, rec.Code, rec.Body.String())
		}
		var body struct {
			Total      int `json:"total"`
			TotalUnits int `json:"total_units"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		got := body.Total
		if strings.Contains(tc.path, "/graph") {
			got = body.TotalUnits
		}
		if got != tc.total {
			t.Fatalf("%s total = %d, want %d (body %s)", tc.path, got, tc.total, rec.Body.String())
		}
	}
}

// A reprocess is a by-id write, so it carries the read/write tag-scope gate a
// retag and a delete do: a document outside the caller's read scope reads as
// not-found (the id echoed, as the reference's write gate does), a readable one
// whose tags the caller may not write is refused, an absent document reads the
// same as one out of scope, and an unscoped caller is unaffected.
func TestReprocessDocumentHonorsTagScope(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	for _, doc := range []struct {
		id   string
		tags []string
	}{
		{"kate-doc", []string{"user:kate"}},
		{"rule-doc", []string{"kind:rule"}},
		{"dan-doc", []string{"user:dan"}},
	} {
		if _, err := store.PutDocument(ctx, bank, doc.id, storage.Document{
			OriginalText: "Alice works at Google.", Tags: doc.tags,
		}); err != nil {
			t.Fatal(err)
		}
	}
	handler := scopedReprocessHandler(t, store)
	base := "/v1/default/banks/" + bank + "/documents"

	rec := sendAs(t, handler, "dan", http.MethodPost, base+"/kate-doc/reprocess")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Document 'kate-doc' not found") {
		t.Fatalf("dan reprocessing kate's document = %d: %s", rec.Code, rec.Body.String())
	}
	// An absent document is answered by the same gate, so it reads the same as
	// one outside the scope rather than leaking that it does not exist.
	rec = sendAs(t, handler, "dan", http.MethodPost, base+"/missing-doc/reprocess")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Document 'missing-doc' not found") {
		t.Fatalf("dan reprocessing an absent document = %d: %s", rec.Code, rec.Body.String())
	}
	rec = sendAs(t, handler, "dan", http.MethodPost, base+"/rule-doc/reprocess")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "You can't write document 'rule-doc' tagged kind:rule") {
		t.Fatalf("dan reprocessing a rule document = %d: %s", rec.Code, rec.Body.String())
	}
	rec = sendAs(t, handler, "dan", http.MethodPost, base+"/dan-doc/reprocess")
	if rec.Code != http.StatusOK {
		t.Fatalf("dan reprocessing his document = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// An unscoped caller (an administrator) reprocesses any document.
	rec = sendAs(t, handler, "", http.MethodPost, base+"/kate-doc/reprocess")
	if rec.Code != http.StatusOK {
		t.Fatalf("unscoped reprocess = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Without a scope the absent document keeps the plain not-found.
	rec = sendAs(t, handler, "", http.MethodPost, base+"/missing-doc/reprocess")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Document not found") {
		t.Fatalf("unscoped reprocess of an absent document = %d: %s", rec.Code, rec.Body.String())
	}
}

// scopedReprocessHandler wraps the route table with the tag-scope middleware and
// a reprocessor, so the reprocess route's own gate is what the scope reaches.
func scopedReprocessHandler(t *testing.T, store *storage.MemoryStore) http.Handler {
	t.Helper()
	host := httpx.NewExtensionHost()
	host.RegisterOperationValidator(httpx.UserTagScopeValidator{})
	mux := http.NewServeMux()
	handlers := NewHandlers(store)
	handlers.Reprocess = reprocessor{store: store}
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			handler(w, r)
		})
	})
	return httpx.OperationValidationMiddleware(host)(mux)
}
