package memoryops

import (
	"net/http"
	"testing"
)

// An append needs a document to append to. The reference refuses one that names
// none with its own 400 sentence, before it reads the bank's store_document_text
// setting — accepting it (this build's earlier behaviour) stored a new document
// whose text silently replaced rather than appended.
func TestAppendWithoutDocumentIDIsRefused(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"later","update_mode":"append"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	want := `{"detail":"update_mode='append' requires a document_id"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

// The same item with a document_id is an ordinary append and is accepted.
func TestAppendWithDocumentIDIsAccepted(t *testing.T) {
	mux, store := newServer(t)
	if rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"first","document_id":"d1","update_mode":"replace"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"second","document_id":"d1","update_mode":"append"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	document, err := store.GetDocument(t.Context(), "b1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "first\nsecond"; document.OriginalText != want {
		t.Fatalf("original_text = %q, want %q", document.OriginalText, want)
	}
}
