package memoryops

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// An asynchronous retain fixes each item's document before the write is queued.
//
// The extraction runs later, on a worker that no longer holds the request, and
// it attributes its model calls to the document (the reference's
// `set_call_metadata({"document_id": effective_doc_id})`). An id minted during
// the write would be known to nothing outside it, so the row would carry no
// document for an item that named none.
func TestAsyncRetainResolvesDocumentIDsAtSubmission(t *testing.T) {
	mux, store := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"async work"}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// The submission queues a batch parent and its child; the child carries the
	// items.
	payload := map[string]any{}
	for _, op := range page.Operations {
		if items, _ := op.Payload["items"].([]any); len(items) > 0 {
			payload = op.Payload
			break
		}
	}
	if len(payload) == 0 {
		t.Fatalf("no queued operation carries the items: %+v", page.Operations)
	}
	items, _ := payload["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("payload items = %+v", payload["items"])
	}
	item, _ := items[0].(map[string]any)
	if id, _ := item["document_id"].(string); id == "" {
		t.Fatalf("the queued item names no document: %+v", item)
	}
}

// A caller-chosen document id survives submission unchanged.
func TestAsyncRetainKeepsACallerChosenDocumentID(t *testing.T) {
	mux, store := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"async work","document_id":"doc-chosen"}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var items []any
	for _, op := range page.Operations {
		if queued, _ := op.Payload["items"].([]any); len(queued) > 0 {
			items = queued
			break
		}
	}
	item, _ := items[0].(map[string]any)
	if id, _ := item["document_id"].(string); id != "doc-chosen" {
		t.Fatalf("document id = %q, want the caller's", id)
	}
}
