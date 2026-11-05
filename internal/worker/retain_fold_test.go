package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/retainfold"
	"github.com/studyzy/wenyuan/internal/storage"
)

// submitFoldAppend queues one append submission for a document and returns its
// parent (what a caller polls) and child (what a worker claims) identifiers.
func submitFoldAppend(t *testing.T, store storage.Store, documentID, content string, mutate func(*storage.RetainRequest)) (string, string) {
	t.Helper()
	req := storage.RetainRequest{
		BankID: "bank-a",
		Items:  []storage.RetainItem{{Content: content, DocumentID: documentID, UpdateMode: "append"}},
	}
	if mutate != nil {
		mutate(&req)
	}
	submission, err := store.SubmitAsyncRetain(context.Background(), req, storage.DefaultRetainSubBatchTokens)
	if err != nil {
		t.Fatal(err)
	}
	children, err := store.GetOperationChildren(context.Background(), "bank-a", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("children = %d, want one", len(children))
	}
	return submission.OperationID, children[0].OperationID
}

// foldConfig builds a consumer whose backoff never blocks a follow-up claim.
func foldConfig() Config {
	return Config{
		WorkerID: "worker-1", MaxRetries: 3, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now:   func() time.Time { return time.Now().UTC() },
		Sleep: func(time.Duration) {},
	}
}

// claimFoldHandle drives one claim cycle by hand: claim, fold, execute, settle.
func claimFoldHandle(t *testing.T, store storage.Store, consumer *Consumer) (storage.Operation, []storage.Operation, bool) {
	t.Helper()
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if errors.Is(err, storage.ErrOperationNotFound) {
		return storage.Operation{}, nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	folded := consumer.foldClaimedRetain(ctx, &op)
	consumer.handle(ctx, op, folded...)
	return op, folded, true
}

// pendingRetainChildren counts the bank's queued retain children still waiting
// for a claim. Batch parents sit pending too, but nothing claims them.
func pendingRetainChildren(t *testing.T, store storage.Store) int {
	t.Helper()
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: "bank-a", Status: string(storage.StatusPending), Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, op := range page.Operations {
		if op.OperationType == storage.OperationTypeRetain {
			count++
		}
	}
	return count
}

// payloadItemCount decodes a payload's queued items.
func payloadItemCount(t *testing.T, payload map[string]any) int {
	t.Helper()
	req, err := storage.DecodeAsyncRetainPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	return len(req.Items)
}

// A document's queued appends join into one execution: the claimed payload
// carries every member's items and the member list, the row's own payload
// stays untouched, and every member — and its parent — settles together.
func TestFoldJoinsQueuedAppendsIntoOneExecution(t *testing.T) {
	store := newStore(t)
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

	op, folded, ok := claimFoldHandle(t, store, consumer)
	if !ok {
		t.Fatal("nothing claimed")
	}
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
	for _, ref := range refs {
		if ref.ItemsCount != 1 {
			t.Fatalf("member %s carried %d items, want one", ref.OperationID, ref.ItemsCount)
		}
	}
	if got := payloadItemCount(t, op.Payload); got != 3 {
		t.Fatalf("payload lost its merged items: %d", got)
	}
	if len(ranItems) != 1 || ranItems[0] != 3 {
		t.Fatalf("executions = %v, want one run over three items", ranItems)
	}

	// The row keeps its own submission: a reclaim would run it unfolded.
	stored, err := store.GetOperation(context.Background(), "bank-a", op.OperationID)
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
		after, err := store.GetOperation(context.Background(), "bank-a", peer.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != storage.StatusCompleted {
			t.Fatalf("folded peer %s = %s, want completed", peer.OperationID, after.Status)
		}
	}
	for _, parent := range parents {
		after, err := store.GetOperation(context.Background(), "bank-a", parent)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != storage.StatusCompleted {
			t.Fatalf("parent %s = %s, want completed", parent, after.Status)
		}
	}
}

// A submission that is not append-only runs alone, and everything queued
// behind it waits for a later claim.
func TestFoldLeavesNonAppendSubmissionsAlone(t *testing.T) {
	store := newStore(t)
	submitFoldAppend(t, store, "doc-1", "swap the body", func(req *storage.RetainRequest) {
		req.Items[0].UpdateMode = "replace"
	})
	submitFoldAppend(t, store, "doc-1", "one more turn", nil)

	var ranItems []int
	consumer := New(store, ProcessorFunc(func(_ context.Context, op storage.Operation) error {
		ranItems = append(ranItems, payloadItemCount(t, op.Payload))
		return nil
	}), foldConfig())

	if _, folded, ok := claimFoldHandle(t, store, consumer); !ok || len(folded) != 0 {
		t.Fatalf("folded = %d (claimed %v), want none: a replace is not a fold base", len(folded), ok)
	}
	if len(ranItems) != 1 || ranItems[0] != 1 {
		t.Fatalf("executions = %v, want one run over one item", ranItems)
	}
	if pendingRetainChildren(t, store) != 1 {
		t.Fatal("the queued append must stay pending behind the replace")
	}
	// Drain: both operations complete, each on its own.
	if _, _, ok := claimFoldHandle(t, store, consumer); !ok {
		t.Fatal("the queued append was never claimed")
	}
	if len(ranItems) != 2 || ranItems[1] != 1 {
		t.Fatalf("executions = %v, want two single-item runs", ranItems)
	}
}

// Every way a peer cannot join leaves the fold empty: the primary runs alone
// and the peer waits for its own claim.
func TestFoldRejectsPeerThatCannotJoin(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*storage.RetainRequest)
	}{
		{"peer is not append-only", func(req *storage.RetainRequest) {
			req.Items[0].UpdateMode = "replace"
		}},
		{"peer carries file metadata", func(req *storage.RetainRequest) {
			req.Items[0].Metadata = map[string]any{}
		}},
		{"peer has different document tags", func(req *storage.RetainRequest) {
			req.DocumentTags = []string{"private"}
		}},
		{"peer has different extraction settings", func(req *storage.RetainRequest) {
			req.ExtractionMode = "verbose"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			submitFoldAppend(t, store, "doc-1", "the joiner", tc.mutate)
			submitFoldAppend(t, store, "doc-1", "the base", nil)

			consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error { return nil }), foldConfig())
			op, folded, ok := claimFoldHandle(t, store, consumer)
			if !ok {
				t.Fatal("nothing claimed")
			}
			if len(folded) != 0 {
				t.Fatalf("folded = %d, want none", len(folded))
			}
			// Whichever side claimed, the pair runs one item each.
			if got := payloadItemCount(t, op.Payload); got != 1 {
				t.Fatalf("claimed payload items = %d, want one", got)
			}
			if pendingRetainChildren(t, store) != 1 {
				t.Fatal("the refused operation must wait for its own claim")
			}
			second, _, ok := claimFoldHandle(t, store, consumer)
			if !ok {
				t.Fatal("the refused operation was never claimed")
			}
			if second.SerializationKey != "doc-1" {
				t.Fatalf("second claim = %+v, want the refused operation", second)
			}
			if got := payloadItemCount(t, second.Payload); got != 1 {
				t.Fatalf("second payload items = %d, want one", got)
			}
		})
	}
}

// A folded execution is all-or-nothing, so one poisonous item must not hold
// the whole document hostage: the fold width halves per retry until the group
// runs narrow enough to isolate it.
func TestFoldHalvesWidthOnRetry(t *testing.T) {
	store := newStore(t)
	for i := 0; i < 20; i++ {
		submitFoldAppend(t, store, "doc-1", "queued turn", nil)
	}
	var ranItems []int
	consumer := New(store, ProcessorFunc(func(_ context.Context, op storage.Operation) error {
		ranItems = append(ranItems, payloadItemCount(t, op.Payload))
		return errors.New("extraction failed")
	}), foldConfig())

	if _, folded, ok := claimFoldHandle(t, store, consumer); !ok || len(folded) != 16 {
		t.Fatalf("first fold = %d peers (claimed %v), want the full width of sixteen", len(folded), ok)
	}
	// The whole group goes back to pending and is re-claimed together.
	if pendingRetainChildren(t, store) != 20 {
		t.Fatalf("pending = %d, want all twenty back in the queue", pendingRetainChildren(t, store))
	}
	time.Sleep(2 * time.Millisecond) // let the retry gate pass
	op, folded, ok := claimFoldHandle(t, store, consumer)
	if !ok {
		t.Fatal("nothing re-claimed")
	}
	if op.RetryCount != 1 {
		t.Fatalf("retry count = %d, want one", op.RetryCount)
	}
	if len(folded) != 8 {
		t.Fatalf("second fold = %d peers, want the halved width of eight", len(folded))
	}
	if len(ranItems) != 2 || ranItems[0] != 17 || ranItems[1] != 9 {
		t.Fatalf("executions = %v, want seventeen items then nine", ranItems)
	}
	// The second execution failed too, so its nine members are scheduled as
	// well: the eight deferred, the three untouched and the nine that ran.
	if pendingRetainChildren(t, store) != 20 {
		t.Fatalf("pending = %d, want all twenty back in the queue", pendingRetainChildren(t, store))
	}
}

// The token budget bounds what a fold joins, the same per-execution budget the
// queued-submission split applies.
func TestFoldStopsAtTokenBudget(t *testing.T) {
	store := newStore(t)
	const content = "one queued turn of prose"
	for i := 0; i < 3; i++ {
		submitFoldAppend(t, store, "doc-1", content, nil)
	}
	cfg := foldConfig()
	cfg.RetainFoldTokens = 2 * storage.CountTokens(content) // primary + one peer, exactly
	var ranItems []int
	consumer := New(store, ProcessorFunc(func(_ context.Context, op storage.Operation) error {
		ranItems = append(ranItems, payloadItemCount(t, op.Payload))
		return nil
	}), cfg)

	op, folded, ok := claimFoldHandle(t, store, consumer)
	if !ok || len(folded) != 1 {
		t.Fatalf("folded = %d peers (claimed %v), want the one that fits the budget", len(folded), ok)
	}
	if got := payloadItemCount(t, op.Payload); got != 2 {
		t.Fatalf("payload items = %d, want two", got)
	}
	if len(ranItems) != 1 || ranItems[0] != 2 {
		t.Fatalf("executions = %v, want one run over two items", ranItems)
	}
	if pendingRetainChildren(t, store) != 1 {
		t.Fatal("the item past the budget must stay pending")
	}
}

// A fold that cannot claim its peers is an optimization the execution does
// without: the claimed operation runs on its own items and the peers stay
// pending, never lost and never claimed by an execution that ignores them.
func TestFoldFailureRunsTheClaimAlone(t *testing.T) {
	store := newStore(t)
	submitFoldAppend(t, store, "doc-1", "turn one", nil)
	_, peer := submitFoldAppend(t, store, "doc-1", "turn two", nil)
	blocked := &foldBlockingStore{Store: store}
	var ranItems []int
	consumer := New(blocked, ProcessorFunc(func(_ context.Context, op storage.Operation) error {
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

// foldBlockingStore refuses the peer claim, so a planned fold cannot take the
// operations it found pending.
type foldBlockingStore struct {
	storage.Store
}

func (s *foldBlockingStore) ClaimPendingOperations(ctx context.Context, workerID string, ids []string) ([]string, error) {
	return nil, errors.New("peer claim refused")
}

// A deferral holds the whole group, spending no member's attempt budget.
func TestFoldDefersItsMembersTogether(t *testing.T) {
	store := newStore(t)
	parents := make([]string, 0, 3)
	for _, text := range []string{"one", "two", "three"} {
		parent, _ := submitFoldAppend(t, store, "doc-1", text, nil)
		parents = append(parents, parent)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return Defer(errors.New("receiver rate limited"))
	}), foldConfig())

	op, folded, ok := claimFoldHandle(t, store, consumer)
	if !ok || len(folded) != 2 {
		t.Fatalf("folded = %d (claimed %v), want two", len(folded), ok)
	}
	members := append([]storage.Operation{op}, folded...)
	for _, member := range members {
		after, err := store.GetOperation(context.Background(), "bank-a", member.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != storage.StatusPending || after.NextRetryAt == nil || after.RetryCount != 0 {
			t.Fatalf("deferred member %s = %+v, want pending at a scheduled moment with no attempt spent", member.OperationID, after)
		}
	}
	_ = parents
}

// A permanent failure is terminal for every member: they either all committed
// or none did, and each member's parent rolls up.
func TestFoldFailsItsMembersTogether(t *testing.T) {
	store := newStore(t)
	parents := make([]string, 0, 3)
	for _, text := range []string{"one", "two", "three"} {
		parent, _ := submitFoldAppend(t, store, "doc-1", text, nil)
		parents = append(parents, parent)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return Permanent(errors.New("bank refuses writes"))
	}), foldConfig())

	op, folded, ok := claimFoldHandle(t, store, consumer)
	if !ok || len(folded) != 2 {
		t.Fatalf("folded = %d (claimed %v), want two", len(folded), ok)
	}
	for _, member := range append([]storage.Operation{op}, folded...) {
		after, err := store.GetOperation(context.Background(), "bank-a", member.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != storage.StatusFailed || after.Error == "" {
			t.Fatalf("member %s = %s (%q), want failed with the cause", member.OperationID, after.Status, after.Error)
		}
	}
	for _, parent := range parents {
		after, err := store.GetOperation(context.Background(), "bank-a", parent)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != storage.StatusFailed {
			t.Fatalf("parent %s = %s, want failed", parent, after.Status)
		}
	}
}
