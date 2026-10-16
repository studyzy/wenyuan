//go:build pgx

package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The PostgreSQL backend deletes a bank's queued operations with the bank, so a
// worker cannot pick up work whose bank is gone. It gets this from the
// async_operations foreign-key cascade, unlike the memory backend, which purges
// the queue itself — the two must agree on the observable result.
func TestPostgresDeleteBankCascadesQueuedOperations(t *testing.T) {
	pg := postgresVectorStore(t).(*PostgresStore)
	store := Store(pg)
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "q-bank", Name: "q-bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "11111111-1111-1111-1111-111111111111", BankID: "q-bank", OperationType: AsyncRetainType,
		Status: StatusPending, Run: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteBank(ctx, "q-bank"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetOperation(ctx, "q-bank", "11111111-1111-1111-1111-111111111111"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("the foreign-key cascade must remove a deleted bank's queue, got %v", err)
	}
	alive, err := pg.OperationAlive(ctx, "q-bank", "11111111-1111-1111-1111-111111111111")
	if err != nil || alive {
		t.Fatalf("a queued operation of a deleted bank must read dead, got alive=%v err=%v", alive, err)
	}
}

// A queued retain's write must accept its own operation record: the worker owns
// the child, so the row is this write, not a reused identifier.
func TestPostgresRetainWriteSkipOperationRecordToleratesOwnQueuedOperation(t *testing.T) {
	pg := postgresVectorStore(t).(*PostgresStore)
	store := Store(pg)
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "q-bank", Name: "q-bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "22222222-2222-2222-2222-222222222222", BankID: "q-bank", OperationType: AsyncRetainType,
		Status: StatusProcessing, WorkerID: "worker-1", Run: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID:              "q-bank",
		OperationID:         "22222222-2222-2222-2222-222222222222",
		SkipOperationRecord: true,
		Items:               []RetainItem{{Content: "Alice moved to Berlin."}},
	})
	if err != nil {
		t.Fatalf("a queued write must accept its own operation record: %v", err)
	}
	if len(outcome.ItemUnitIDs) != 1 || len(outcome.ItemUnitIDs[0]) == 0 {
		t.Fatalf("outcome = %+v, want one unit written", outcome)
	}
}

// OperationAlive reports a cancelled operation as dead and a live one as alive.
func TestPostgresOperationAliveReportsCancellation(t *testing.T) {
	pg := postgresVectorStore(t).(*PostgresStore)
	store := Store(pg)
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "q-bank", Name: "q-bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "33333333-3333-3333-3333-333333333333", BankID: "q-bank", OperationType: AsyncRetainType,
		Status: StatusProcessing, WorkerID: "worker-1", Run: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if alive, err := pg.OperationAlive(ctx, "q-bank", "33333333-3333-3333-3333-333333333333"); err != nil || !alive {
		t.Fatalf("a live operation must read alive, got alive=%v err=%v", alive, err)
	}
	if _, err := store.CancelOperation(ctx, "q-bank", "33333333-3333-3333-3333-333333333333"); err != nil {
		t.Fatal(err)
	}
	if alive, err := pg.OperationAlive(ctx, "q-bank", "33333333-3333-3333-3333-333333333333"); err != nil || alive {
		t.Fatalf("a cancelled operation must read dead, got alive=%v err=%v", alive, err)
	}
}
