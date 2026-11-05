package storage

import (
	"context"
	"testing"
	"time"
)

// submitRetainChildren queues one async retain submission and returns its
// child operation identifier — the row a worker claims and a fold joins.
func submitRetainChildren(t *testing.T, store Store, items []RetainItem) (string, string) {
	t.Helper()
	submission, err := store.SubmitAsyncRetain(context.Background(), RetainRequest{
		BankID: "fold-bank",
		Items:  items,
	}, DefaultRetainSubBatchTokens)
	if err != nil {
		t.Fatal(err)
	}
	children, err := store.GetOperationChildren(context.Background(), "fold-bank", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("children = %d, want one", len(children))
	}
	return submission.OperationID, children[0].OperationID
}

func appendItemFor(content, documentID string) RetainItem {
	return RetainItem{Content: content, DocumentID: documentID, UpdateMode: "append"}
}

// PendingRetainPeers is the fold's candidate list: only pending retain rows on
// the bank and key asked for, oldest first, ties by identifier, bounded by the
// limit, each carrying its payload.
func TestPendingRetainPeersFiltersAndOrders(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	_, first := submitRetainChildren(t, store, []RetainItem{appendItemFor("one", "doc-1")})
	_, second := submitRetainChildren(t, store, []RetainItem{appendItemFor("two", "doc-1")})
	_, otherDoc := submitRetainChildren(t, store, []RetainItem{appendItemFor("elsewhere", "doc-2")})
	_, replaceMode := submitRetainChildren(t, store, []RetainItem{{Content: "swap", DocumentID: "doc-1", UpdateMode: "replace"}})
	// A submission that spans two documents carries no key at all.
	_, keyless := submitRetainChildren(t, store, []RetainItem{
		{Content: "a", DocumentID: "doc-x", UpdateMode: "append"},
		{Content: "b", DocumentID: "doc-y", UpdateMode: "append"},
	})

	// Push each child onto its own clock so the order is decided by created_at,
	// not by the identifier tiebreak.
	memory := store
	memory.mu.Lock()
	shifted := memory.operations[first]
	shifted.CreatedAt = shifted.CreatedAt.Add(-time.Minute)
	memory.operations[first] = shifted
	shifted = memory.operations[replaceMode]
	shifted.CreatedAt = shifted.CreatedAt.Add(time.Minute)
	memory.operations[replaceMode] = shifted
	memory.mu.Unlock()

	// A replace is still a queued retain for the document: it is a candidate
	// the fold plan later refuses, not one the candidate list filters out.
	peers, err := store.PendingRetainPeers(ctx, "fold-bank", "doc-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 3 {
		t.Fatalf("peers = %d, want the three doc-1 rows", len(peers))
	}
	if peers[0].OperationID != first || peers[1].OperationID != second || peers[2].OperationID != replaceMode {
		t.Fatalf("peers = [%s, %s, %s], want the oldest first", peers[0].OperationID, peers[1].OperationID, peers[2].OperationID)
	}
	for _, peer := range peers {
		if peer.OperationType != OperationTypeRetain || peer.Payload == nil {
			t.Fatalf("peer missing its payload: %+v", peer)
		}
	}
	_ = otherDoc
	_ = keyless

	if limited, err := store.PendingRetainPeers(ctx, "fold-bank", "doc-1", 1); err != nil || len(limited) != 1 {
		t.Fatalf("limit = %d peers (err %v), want one", len(limited), err)
	}
	if gated, err := store.PendingRetainPeers(ctx, "fold-bank", "doc-1", 0); err != nil || len(gated) != 0 {
		t.Fatalf("a non-positive limit must ask for nothing, got %d (err %v)", len(gated), err)
	}

	// A pending row whose retry gate has not passed is not a candidate yet.
	memory.mu.Lock()
	gatedOp := memory.operations[second]
	future := time.Now().UTC().Add(time.Hour)
	gatedOp.NextRetryAt = &future
	memory.operations[second] = gatedOp
	memory.mu.Unlock()
	after, err := store.PendingRetainPeers(ctx, "fold-bank", "doc-1", 10)
	if err != nil || len(after) != 2 || after[0].OperationID != first || after[1].OperationID != replaceMode {
		t.Fatalf("a gated row must wait, got %v (err %v)", after, err)
	}
}

// ClaimPendingOperations claims the still-pending rows it is given and skips
// the rest, so a caller folds only the operations it actually holds.
func TestClaimPendingOperationsClaimsOnlyPending(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	_, one := submitRetainChildren(t, store, []RetainItem{appendItemFor("one", "doc-1")})
	_, two := submitRetainChildren(t, store, []RetainItem{appendItemFor("two", "doc-1")})
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPendingOperations(ctx, "worker-2", []string{one, two, "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0] != two {
		t.Fatalf("claimed = %v, want only the still-pending row", claimed)
	}
	held, err := store.GetOperation(ctx, "fold-bank", two)
	if err != nil {
		t.Fatal(err)
	}
	if held.Status != StatusProcessing || held.WorkerID != "worker-2" {
		t.Fatalf("claimed row = %+v, want processing under worker-2", held)
	}
	if again, err := store.ClaimPendingOperations(ctx, "worker-2", []string{two}); err != nil || len(again) != 0 {
		t.Fatalf("a claimed row must not be claimed twice, got %v (err %v)", again, err)
	}
}
