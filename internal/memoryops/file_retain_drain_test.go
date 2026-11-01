package memoryops

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// drainQueuedFileRetains runs every queued file upload's retain half, the way
// the worker would. The route is asynchronous — the conversion happens in the
// request and the extraction in the worker — so a test that reads the store
// straight after the response must drain the queue first. The worker's own path
// is exercised end to end in internal/app; here it is replayed directly so these
// route tests stay package-local.
//
// A file upload is tracked by one file_convert_retain operation, and that row is
// what the route returned, so the drain settles that row in place rather than
// looking for a separate retain row beside it.
func drainQueuedFileRetains(t *testing.T, store *storage.MemoryStore) {
	t.Helper()
	ctx := context.Background()
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range page.Operations {
		if op.OperationType != storage.OperationTypeFileConvertRetain || op.Status.Terminal() {
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
		if _, err := store.AdvanceOperation(ctx, op.OperationID, storage.StatusCompleted, ""); err != nil {
			t.Fatalf("settle queued file retain %s: %v", op.OperationID, err)
		}
	}
}
