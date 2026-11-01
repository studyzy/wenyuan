package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// fileRetainOperationIDs decodes the route's acceptance body: it is the
// operation_ids list, one entry per uploaded file.
func fileRetainOperationIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var body struct {
		OperationIDs []string `json:"operation_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return body.OperationIDs
}

// Every identifier the acceptance response hands back must name one operation
// the caller can follow through to its outcome. The route used to return the
// identifier of a conversion record it had already completed while the retain
// it belonged to was queued as a separate, unlinked row, so polling what the
// caller was given reported finished work that had not started.
func TestFileRetainReturnsTheOperationItQueued(t *testing.T) {
	mux, store := configuredServer(t, nil)
	req, contentType := uploadFiles(t, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	ids := fileRetainOperationIDs(t, rec)
	if len(ids) != 2 {
		t.Fatalf("operation_ids = %v, want one per file", ids)
	}
	ctx := context.Background()
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != len(ids) {
		t.Fatalf("the route left %d operation row(s) for %d identifier(s): %+v",
			len(page.Operations), len(ids), page.Operations)
	}
	for _, id := range ids {
		op, err := store.GetOperation(ctx, "b1", id)
		if err != nil {
			t.Fatalf("the returned identifier %q names no operation: %v", id, err)
		}
		if op.Status != storage.StatusPending {
			t.Fatalf("operation %q is %s, want pending work the caller can follow", id, op.Status)
		}
		if op.OperationType != storage.OperationTypeFileConvertRetain {
			t.Fatalf("operation %q is type %q, want the file conversion-and-retain operation",
				id, op.OperationType)
		}
		if _, err := storage.DecodeAsyncRetainPayload(op.Payload); err != nil {
			t.Fatalf("operation %q carries no runnable retain: %v", id, err)
		}
	}
}

// The same identifier must still name the operation once the work settles: the
// conversion and the retain are one row, so completion is that row's terminal
// status and its units are the document's memories.
func TestFileRetainIdentifiersSurviveCompletion(t *testing.T) {
	mux, store := configuredServer(t, nil)
	rec := uploadFile(t, mux, "notes.txt", "the prose", "text/plain")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	ids := fileRetainOperationIDs(t, rec)
	if len(ids) != 1 {
		t.Fatalf("operation_ids = %v, want one", ids)
	}
	ctx := context.Background()
	pending, err := store.GetOperation(ctx, "b1", ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != storage.StatusPending {
		t.Fatalf("operation %q is %s before the worker runs, want pending", ids[0], pending.Status)
	}
	drainQueuedFileRetains(t, store)
	finished, err := store.GetOperation(ctx, "b1", ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != storage.StatusCompleted {
		t.Fatalf("operation %q settled as %s, want completed", ids[0], finished.Status)
	}
	page, err := store.ListMemories(ctx, storage.MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Memories) == 0 {
		t.Fatal("the completed operation left the retain unrun")
	}
}
