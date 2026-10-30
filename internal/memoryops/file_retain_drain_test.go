package memoryops

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// drainQueuedFileRetains runs the retain half of every file upload the route
// queued. The route is asynchronous — the conversion happens in the request and
// the extraction in the worker — so a test that reads the store straight after
// the response must drain the queue first. The worker's own path is exercised
// end to end in internal/app; here it is replayed directly so these route tests
// stay package-local.
func drainQueuedFileRetains(t *testing.T, store *storage.MemoryStore) {
	t.Helper()
	ctx := context.Background()
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range page.Operations {
		if op.OperationType != storage.OperationTypeRetain || op.Status.Terminal() {
			continue
		}
		req, err := storage.DecodeAsyncRetainPayload(op.Payload)
		if err != nil {
			t.Fatalf("decode queued file retain %s: %v", op.OperationID, err)
		}
		req.BankID = op.BankID
		if _, err := store.RetainWrite(ctx, req); err != nil {
			t.Fatalf("run queued file retain %s: %v", op.OperationID, err)
		}
		op.Status = storage.StatusCompleted
		if _, err := store.PutOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}
}
