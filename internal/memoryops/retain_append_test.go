package memoryops

import (
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
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

// The reference validates the append inside submit_async_retain too (its
// _validate_retain_update_modes), so an async append that names no document is
// refused at submit with the same 400 sentence instead of queueing work whose
// worker mints the append a fresh document after the caller saw success.
func TestAsyncAppendWithoutDocumentIDIsRefusedAtSubmit(t *testing.T) {
	mux, store := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"later","update_mode":"append"}],"async":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	want := `{"detail":"update_mode='append' requires a document_id"}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
	// Nothing may be queued: the refusal is the whole answer.
	page, err := store.ListOperations(t.Context(), storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("a refused submission queued %d operations", len(page.Operations))
	}
}

// An async append that names its document still queues.
func TestAsyncAppendWithDocumentIDQueues(t *testing.T) {
	mux, _ := newServer(t)
	if rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"first","document_id":"d1","update_mode":"replace"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"second","document_id":"d1","update_mode":"append"}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if decodeOpID(t, rec) == "" {
		t.Fatalf("no operation queued: %s", rec.Body.String())
	}
}
