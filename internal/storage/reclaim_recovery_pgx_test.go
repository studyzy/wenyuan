//go:build pgx

package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The PostgreSQL claim counts lease-expiry recovery exactly as the in-memory
// one does: an abandoned claim spends one attempt, and a spent budget fails the
// row with the reference's sentence instead of running it again.
func TestPostgresClaimReclaimsCountTowardTheRecoveryBudget(t *testing.T) {
	pg, _ := newClearMemoriesStore(t, "reclaim")
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-2 * time.Hour)

	// At the cap: the claim fails the row rather than running it again.
	spent := newID()
	if _, err := pg.PutOperation(ctx, Operation{
		OperationID: spent, BankID: "b1", OperationType: OperationTypeConsolidate,
		Status: StatusProcessing, Payload: map[string]any{"k": "v"},
		WorkerID: "worker-0", ClaimedAt: &expired, RetryCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.ClaimNextOperation(ctx, "worker-1", time.Hour, WithReclaimLimit(3)); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("claim = %v, want the spent row failed and nothing claimable", err)
	}
	after, err := pg.GetOperation(ctx, "b1", spent)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", after.Status)
	}
	if want := "exceeded max recovery attempts (retry_count >= 3)"; after.Error != want {
		t.Fatalf("error = %q, want %q", after.Error, want)
	}
	if after.CompletedAt == nil {
		t.Fatal("completed_at = nil, want it stamped by the recovery failure")
	}

	// Under the cap: the claim recovers the row and spends one attempt.
	under := newID()
	if _, err := pg.PutOperation(ctx, Operation{
		OperationID: under, BankID: "b1", OperationType: OperationTypeConsolidate,
		Status: StatusProcessing, Payload: map[string]any{"k": "v"},
		WorkerID: "worker-0", ClaimedAt: &expired, RetryCount: 2,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := pg.ClaimNextOperation(ctx, "worker-1", time.Hour, WithReclaimLimit(3))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.OperationID != under || claimed.RetryCount != 3 {
		t.Fatalf("claimed = %+v, want the abandoned row recovered with its attempt spent", claimed)
	}

	// A fresh pending row spends nothing.
	fresh := newID()
	if _, err := pg.PutOperation(ctx, Operation{
		OperationID: fresh, BankID: "b1", OperationType: OperationTypeRetain,
		Status: StatusPending, Payload: map[string]any{"k": "v"}, RetryCount: 9,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err = pg.ClaimNextOperation(ctx, "worker-1", time.Hour, WithReclaimLimit(3))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.OperationID != fresh || claimed.RetryCount != 9 {
		t.Fatalf("claimed = %+v, want the pending row claimed without spending", claimed)
	}
}
