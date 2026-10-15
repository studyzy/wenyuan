package storage

import (
	"context"
	"testing"
	"time"
)

// A submission queues one parent that aggregates it and one child per
// token-bounded sub-batch, and the parent carries no work a worker could run.
func TestSubmitAsyncRetainQueuesParentAndChild(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	submission, err := store.SubmitAsyncRetain(ctx, RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "Alice moved to Berlin.", DocumentID: "doc-1"}},
	}, DefaultRetainSubBatchTokens)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := store.GetOperation(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.OperationType != AsyncRetainType || parent.Status != StatusPending {
		t.Fatalf("parent = %+v, want a pending batch_retain", parent)
	}
	if !operationIsParent(parent.ResultMetadata) || intFromMetadata(parent.ResultMetadata, "items_count") != 1 {
		t.Fatalf("parent metadata = %+v, want items_count 1 and is_parent", parent.ResultMetadata)
	}
	if len(parent.Payload) != 0 {
		t.Fatalf("a parent must carry no task payload, got %+v", parent.Payload)
	}
	children, err := store.GetOperationChildren(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].Status != StatusPending {
		t.Fatalf("children = %+v, want one pending child", children)
	}
	// The parent is a status aggregator, so no claim may take it: claiming one
	// would mark it processing and then settle it as if it had run.
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatalf("the child must be claimable: %v", err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-2", time.Hour); err == nil {
		t.Fatal("the parent must not be claimable")
	}
}

// A child that targets one document is serialized on it, so two children of the
// same document never run beside each other; one that spans several documents
// has nothing to be serialized on.
func TestSubmitAsyncRetainSerializesPerDocument(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	single, err := store.SubmitAsyncRetain(ctx, RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "first", DocumentID: "session-1"}},
	}, DefaultRetainSubBatchTokens)
	if err != nil {
		t.Fatal(err)
	}
	children, err := store.GetOperationChildren(ctx, "b1", single.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("children = %+v, want one", children)
	}
	child, err := store.GetOperation(ctx, "b1", children[0].OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if child.SerializationKey != "session-1" {
		t.Fatalf("serialization key = %q, want the document it targets", child.SerializationKey)
	}
	spread, err := store.SubmitAsyncRetain(ctx, RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "a", DocumentID: "doc-a"}, {Content: "b", DocumentID: "doc-b"}},
	}, DefaultRetainSubBatchTokens)
	if err != nil {
		t.Fatal(err)
	}
	children, err = store.GetOperationChildren(ctx, "b1", spread.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	child, err = store.GetOperation(ctx, "b1", children[0].OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if child.SerializationKey != "" {
		t.Fatalf("a multi-document child must not be serialized, got %q", child.SerializationKey)
	}
}

// Resubmitting a caller-supplied identifier rejoins the original submission
// instead of queueing the work twice, and an identifier another bank owns is a
// conflict rather than a silent overwrite.
func TestSubmitAsyncRetainIsIdempotentOnTheIdentifier(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	req := RetainRequest{
		BankID:      "b1",
		Items:       []RetainItem{{Content: "Alice moved to Berlin."}},
		OperationID: "11111111-1111-1111-1111-111111111111",
	}
	first, err := store.SubmitAsyncRetain(ctx, req, DefaultRetainSubBatchTokens)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SubmitAsyncRetain(ctx, req, DefaultRetainSubBatchTokens)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Deduplicated || second.OperationID != first.OperationID {
		t.Fatalf("a resubmission must rejoin the original: %+v", second)
	}
	page, err := store.ListOperations(ctx, OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("a resubmission must queue nothing new, got %d operations", page.Total)
	}
	other := req
	other.BankID = "b2"
	if _, err := store.SubmitAsyncRetain(ctx, other, DefaultRetainSubBatchTokens); err == nil {
		t.Fatal("an identifier another bank owns must be a conflict")
	}
}

// The parent reaches a terminal state only when its last child does, and the
// roll-up folds the children's outcome counters without doubling the
// submission's own item count.
func TestFinalizeBatchParentRollsChildrenUp(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	submission, err := store.SubmitAsyncRetain(ctx, RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "Alice moved to Berlin."}, {Content: "Alice plays cello."}},
	}, 1) // a one-token budget splits the two items into two children
	if err != nil {
		t.Fatal(err)
	}
	children, err := store.GetOperationChildren(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %+v, want the batch split in two", children)
	}
	for index, child := range children {
		if _, err := store.AdvanceOperationWithResult(ctx, child.OperationID, StatusCompleted, "",
			map[string]any{"unit_ids_count": index + 1, "extraction_errors_count": 0}); err != nil {
			t.Fatal(err)
		}
		if _, promoted, err := store.FinalizeBatchParent(ctx, "b1", submission.OperationID); err != nil {
			t.Fatal(err)
		} else if promoted != (index == len(children)-1) {
			t.Fatalf("promotion at child %d = %v, want %v", index, promoted, index == len(children)-1)
		}
	}
	parent, err := store.GetOperation(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != StatusCompleted || parent.CompletedAt == nil {
		t.Fatalf("parent = %+v, want completed with a completion time", parent)
	}
	if got := intFromMetadata(parent.ResultMetadata, "items_count"); got != 2 {
		t.Fatalf("items_count = %d, want the submission's own count of 2", got)
	}
	if got := intFromMetadata(parent.ResultMetadata, "unit_ids_count"); got != 3 {
		t.Fatalf("unit_ids_count = %d, want the children's total of 3", got)
	}
	if got := intFromMetadata(parent.ResultMetadata, "extraction_errors_count"); got != 0 {
		t.Fatalf("extraction_errors_count = %d, want 0", got)
	}
}

// A failed child fails the parent once every sibling is done, and retrying the
// parent re-runs the child that failed rather than queueing a second batch.
func TestRetryBatchParentRequeuesFailedChildren(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	submission, err := store.SubmitAsyncRetain(ctx, RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "Alice moved to Berlin."}},
	}, DefaultRetainSubBatchTokens)
	if err != nil {
		t.Fatal(err)
	}
	children, err := store.GetOperationChildren(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceOperation(ctx, children[0].OperationID, StatusFailed, "extraction failed"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FinalizeBatchParent(ctx, "b1", submission.OperationID); err != nil {
		t.Fatal(err)
	}
	parent, err := store.GetOperation(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != StatusFailed || parent.Error == "" {
		t.Fatalf("parent = %+v, want failed with the child's reason", parent)
	}
	if _, err := store.RetryOperation(ctx, "b1", submission.OperationID); err != nil {
		t.Fatalf("retrying a failed batch must requeue its children: %v", err)
	}
	child, err := store.GetOperation(ctx, "b1", children[0].OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Status != StatusPending || child.Error != "" {
		t.Fatalf("child = %+v, want it back in the queue without its old reason", child)
	}
	parent, err = store.GetOperation(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != StatusPending {
		t.Fatalf("parent = %+v, want it revived over its children", parent)
	}
}

// A deferred operation keeps its place in the queue without being recorded as a
// failure: a caller waiting for a bank to settle must not read backpressure as
// a lost operation.
func TestDeferOperationRecordsNoFailure(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "op-1", BankID: "b1", OperationType: OperationTypeRetain, Status: StatusProcessing,
	}); err != nil {
		t.Fatal(err)
	}
	next := time.Now().UTC().Add(time.Minute)
	if err := store.DeferOperation(ctx, "op-1", next, map[string]any{"append_conflict_defers": 1}); err != nil {
		t.Fatal(err)
	}
	op, err := store.GetOperation(ctx, "b1", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != StatusPending || op.NextRetryAt == nil || op.Error != "" || op.RetryCount != 0 {
		t.Fatalf("deferred operation = %+v, want pending with no failure and no budget spent", op)
	}
	if got := intFromMetadata(op.ResultMetadata, "append_conflict_defers"); got != 1 {
		t.Fatalf("the attempt's report must survive the deferral, got %+v", op.ResultMetadata)
	}
}
