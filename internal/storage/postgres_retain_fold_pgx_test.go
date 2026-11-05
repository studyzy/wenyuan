//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
)

// The fold's candidate list against the real queue: only pending retain rows
// on the bank and key asked for, oldest first with the identifier breaking
// ties, bounded by the limit, each carrying its payload.
func TestPendingRetainPeersFiltersAndOrdersPG(t *testing.T) {
	store := postgresVectorStore(t).(*PostgresStore)
	ctx := context.Background()
	bank := "fold-pg-bank"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatal(err)
	}

	submit := func(items ...RetainItem) string {
		submission, err := store.SubmitAsyncRetain(ctx, RetainRequest{BankID: bank, Items: items}, DefaultRetainSubBatchTokens)
		if err != nil {
			t.Fatal(err)
		}
		children, err := store.GetOperationChildren(ctx, bank, submission.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if len(children) != 1 {
			t.Fatalf("children = %d, want one", len(children))
		}
		return children[0].OperationID
	}
	first := submit(RetainItem{Content: "one", DocumentID: "doc-1", UpdateMode: "append"})
	second := submit(RetainItem{Content: "two", DocumentID: "doc-1", UpdateMode: "append"})
	otherDoc := submit(RetainItem{Content: "elsewhere", DocumentID: "doc-2", UpdateMode: "append"})
	keyless := submit(
		RetainItem{Content: "a", DocumentID: "doc-x", UpdateMode: "append"},
		RetainItem{Content: "b", DocumentID: "doc-y", UpdateMode: "append"},
	)
	_ = otherDoc
	_ = keyless

	peers, err := store.PendingRetainPeers(ctx, bank, "doc-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("peers = %d, want the two doc-1 rows", len(peers))
	}
	if peers[0].OperationID != first || peers[1].OperationID != second {
		t.Fatalf("peers = [%s, %s], want submission order", peers[0].OperationID, peers[1].OperationID)
	}
	for _, peer := range peers {
		if peer.OperationType != OperationTypeRetain || peer.Payload == nil || peer.SerializationKey != "doc-1" {
			t.Fatalf("peer is not a queued retain for the document: %+v", peer)
		}
	}

	// A row whose retry gate has not passed waits, and the limit bounds the
	// list.
	future := time.Now().UTC().Add(time.Hour)
	qualified := quoteIdent(store.defaultSchema)
	if _, err := store.db.ExecContext(ctx,
		"UPDATE "+qualified+".async_operations SET next_retry_at = $1 WHERE operation_id = $2", future, second); err != nil {
		t.Fatal(err)
	}
	after, err := store.PendingRetainPeers(ctx, bank, "doc-1", 10)
	if err != nil || len(after) != 1 || after[0].OperationID != first {
		t.Fatalf("a gated row must wait, got %v (err %v)", after, err)
	}
	if _, err := store.db.ExecContext(ctx,
		"UPDATE "+qualified+".async_operations SET next_retry_at = NULL WHERE operation_id = $1", second); err != nil {
		t.Fatal(err)
	}
	if limited, err := store.PendingRetainPeers(ctx, bank, "doc-1", 1); err != nil || len(limited) != 1 {
		t.Fatalf("limit = %d peers (err %v), want one", len(limited), err)
	}
	if none, err := store.PendingRetainPeers(ctx, bank, "doc-1", 0); err != nil || len(none) != 0 {
		t.Fatalf("a non-positive limit must ask for nothing, got %d (err %v)", len(none), err)
	}
}

// ClaimPendingOperations against the real queue: one statement claims the
// still-pending rows it names, skips rows that moved on, and a claimed row is
// never claimed twice.
func TestClaimPendingOperationsClaimsOnlyPendingPG(t *testing.T) {
	store := postgresVectorStore(t).(*PostgresStore)
	ctx := context.Background()
	bank := "fold-pg-claim-bank"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatal(err)
	}
	submit := func(content string) string {
		submission, err := store.SubmitAsyncRetain(ctx, RetainRequest{
			BankID: bank,
			Items:  []RetainItem{{Content: content, DocumentID: "doc-1", UpdateMode: "append"}},
		}, DefaultRetainSubBatchTokens)
		if err != nil {
			t.Fatal(err)
		}
		children, err := store.GetOperationChildren(ctx, bank, submission.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		return children[0].OperationID
	}
	one := submit("one")
	two := submit("two")

	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimPendingOperations(ctx, "worker-2", []string{one, two, "00000000-0000-0000-0000-000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0] != two {
		t.Fatalf("claimed = %v, want only the still-pending row", claimed)
	}
	held, err := store.GetOperation(ctx, bank, two)
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
