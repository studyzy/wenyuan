//go:build pgx

package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// test-claim-payload-predicate-pg: the reference claims only rows whose
// task_payload is present. A pending, non-parent operation inserted with a NULL
// payload — the row the reference leaves pending forever — must not be claimed,
// while an ordinary enqueue (payload an empty object) is.
func TestClaimSkipsNullPayloadOperationPG(t *testing.T) {
	store := postgresVectorStore(t).(*PostgresStore)
	ctx := context.Background()
	qualified := quoteIdent(store.defaultSchema)

	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.async_operations (bank_id, operation_type, status, task_payload)
		 VALUES ('vec-bank', 'retain', 'pending', NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOperation(ctx, "w1", time.Hour); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("a NULL-payload operation must not be claimable, got %v", err)
	}

	if _, err := store.PutOperation(ctx, Operation{
		OperationID: newUUID(), BankID: "vec-bank", OperationType: OperationTypeRetain, Status: StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	op, err := store.ClaimNextOperation(ctx, "w1", time.Hour)
	if err != nil || op.OperationType != OperationTypeRetain {
		t.Fatalf("an enqueued operation must be claimable, got %+v (err %v)", op, err)
	}
}

// test-recover-consolidations-pg mirrors the in-memory predicate check against
// the real SQL: the requeue clears and counts only world/experience facts.
func TestRecoverConsolidationsSkipsNonSourceFactsPG(t *testing.T) {
	store := postgresVectorStore(t).(*PostgresStore)
	ctx := context.Background()
	qualified := quoteIdent(store.defaultSchema)

	for _, row := range []struct{ id, factType string }{
		{"11111111-1111-1111-1111-111111111111", "world"},
		{"22222222-2222-2222-2222-222222222222", "experience"},
		{"33333333-3333-3333-3333-333333333333", "observation"},
	} {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO `+qualified+`.memory_units (id, bank_id, text, fact_type, consolidation_failed_at)
			 VALUES ($1, 'vec-bank', $2, $3, now())`,
			row.id, row.factType, row.factType); err != nil {
			t.Fatal(err)
		}
	}

	retried, err := store.RecoverConsolidations(ctx, "vec-bank")
	if err != nil {
		t.Fatal(err)
	}
	if retried != 2 {
		t.Fatalf("retried = %d, want 2 (world + experience only)", retried)
	}
	var remaining int
	if err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+qualified+`.memory_units
		 WHERE bank_id = 'vec-bank' AND consolidation_failed_at IS NOT NULL`,
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatalf("remaining failed markers = %d, want 1 (the observation)", remaining)
	}
}
