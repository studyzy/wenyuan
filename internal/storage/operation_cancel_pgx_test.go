//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Cancel and retry return a fully scanned operation on a real database.
//
// They are the two paths where a statement returning fewer columns than
// scanOperation reads produced a run-time error rather than a compile error, so
// they are exercised end to end here rather than only by the invariant test.
func TestCancelAndRetryReturnAFullOperation(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the operation-lifecycle test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("op_lifecycle_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	opened, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema, RunMigrations: true, MaxOpenConns: 2})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	store := opened.(*PostgresStore)
	defer func() {
		_ = store.DropSchemaForTest(schema)
		_ = store.Close()
	}()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	// async_operations.operation_id is a uuid column in the reference schema, so
	// the fixture identifies operations by uuid-shaped ids.
	cancelID := pgUUID("op-cancel")
	retryID := pgUUID("op-retry")
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: cancelID, BankID: "b1", OperationType: OperationTypeConsolidate,
		Status: StatusPending, Payload: map[string]any{"k": "v"},
	}); err != nil {
		t.Fatalf("put operation: %v", err)
	}
	cancelled, err := store.CancelOperation(ctx, "b1", cancelID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.OperationID != cancelID || cancelled.Status != StatusCancelled {
		t.Fatalf("cancelled = %+v", cancelled)
	}

	if _, err := store.PutOperation(ctx, Operation{
		OperationID: retryID, BankID: "b1", OperationType: OperationTypeConsolidate,
		Status: StatusFailed, Error: "boom",
	}); err != nil {
		t.Fatalf("put failed operation: %v", err)
	}
	retried, err := store.RetryOperation(ctx, "b1", retryID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retried.OperationID != retryID || retried.Status != StatusPending || retried.RetryCount != 1 {
		t.Fatalf("retried = %+v", retried)
	}
}
