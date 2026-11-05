//go:build pgx

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/retainfold"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The fold against the real queue: a document's queued appends join into one
// execution, the row's own payload stays untouched, and every member — and its
// parent — settles together.
func TestFoldJoinsQueuedAppendsIntoOneExecutionPG(t *testing.T) {
	store := postgresWorkerStore(t)
	ctx := context.Background()
	parents := make([]string, 0, 3)
	for _, text := range []string{"turn one", "turn two", "turn three"} {
		parent, _ := submitFoldAppend(t, store, "doc-1", text, nil)
		parents = append(parents, parent)
	}
	var ranItems []int
	consumer := New(store, ProcessorFunc(func(_ context.Context, op storage.Operation) error {
		ranItems = append(ranItems, payloadItemCount(t, op.Payload))
		return nil
	}), foldConfig())

	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	folded := consumer.foldClaimedRetain(ctx, &op)
	if len(folded) != 2 {
		t.Fatalf("folded peers = %d, want the two queued appends", len(folded))
	}
	if got := payloadItemCount(t, op.Payload); got != 3 {
		t.Fatalf("claimed payload items = %d, want the merged three", got)
	}
	refs, ok := retainfold.RefsFromPayload(op.Payload[retainfold.PayloadKey])
	if !ok || len(refs) != 3 {
		t.Fatalf("payload members = %v (present %v), want three", refs, ok)
	}
	if len(ranItems) != 0 {
		t.Fatal("nothing has executed yet")
	}
	consumer.handle(ctx, op, folded...)
	if len(ranItems) != 1 || ranItems[0] != 3 {
		t.Fatalf("executions = %v, want one run over three items", ranItems)
	}

	// The row keeps its own submission: the merged payload lived only in the
	// execution's hands.
	stored, err := store.GetOperation(ctx, "bank-a", op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if payloadItemCount(t, stored.Payload) != 1 {
		t.Fatal("the claimed payload must never be persisted in merged form")
	}
	if _, present := stored.Payload[retainfold.PayloadKey]; present {
		t.Fatal("the row must not carry a member list")
	}
	for _, peer := range folded {
		after, err := store.GetOperation(ctx, "bank-a", peer.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != storage.StatusCompleted {
			t.Fatalf("folded peer %s = %s, want completed", peer.OperationID, after.Status)
		}
	}
	for _, parent := range parents {
		after, err := store.GetOperation(ctx, "bank-a", parent)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != storage.StatusCompleted {
			t.Fatalf("parent %s = %s, want completed", parent, after.Status)
		}
	}
}

// A peer that cannot join keeps its own execution against the real queue too.
func TestFoldRejectsPeerThatCannotJoinPG(t *testing.T) {
	store := postgresWorkerStore(t)
	submitFoldAppend(t, store, "doc-1", "swap the body", func(req *storage.RetainRequest) {
		req.Items[0].UpdateMode = "replace"
	})
	submitFoldAppend(t, store, "doc-1", "one more turn", nil)

	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error { return nil }), foldConfig())
	op, folded, ok := claimFoldHandle(t, store, consumer)
	if !ok {
		t.Fatal("nothing claimed")
	}
	if len(folded) != 0 {
		t.Fatalf("folded = %d, want none", len(folded))
	}
	if got := payloadItemCount(t, op.Payload); got != 1 {
		t.Fatalf("claimed payload items = %d, want one", got)
	}
	if pendingRetainChildren(t, store) != 1 {
		t.Fatal("the refused operation must wait for its own claim")
	}
	if _, _, ok := claimFoldHandle(t, store, consumer); !ok {
		t.Fatal("the refused operation was never claimed")
	}
}

// A fold whose peer claim fails leaves the execution unfolded and the peers
// pending, against the real queue as well.
func TestFoldFailureRunsTheClaimAlonePG(t *testing.T) {
	store := postgresWorkerStore(t)
	submitFoldAppend(t, store, "doc-1", "turn one", nil)
	_, peer := submitFoldAppend(t, store, "doc-1", "turn two", nil)

	var ranItems []int
	consumer := New(&foldBlockingStore{Store: store}, ProcessorFunc(func(_ context.Context, op storage.Operation) error {
		ranItems = append(ranItems, payloadItemCount(t, op.Payload))
		return nil
	}), foldConfig())

	_, folded, ok := claimFoldHandle(t, store, consumer)
	if !ok || len(folded) != 0 {
		t.Fatalf("folded = %d (claimed %v), want none: the fold is skipped, not fatal", len(folded), ok)
	}
	if len(ranItems) != 1 || ranItems[0] != 1 {
		t.Fatalf("executions = %v, want one run over one item", ranItems)
	}
	after, err := store.GetOperation(context.Background(), "bank-a", peer)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != storage.StatusPending {
		t.Fatalf("the peer = %s, want pending", after.Status)
	}
}
