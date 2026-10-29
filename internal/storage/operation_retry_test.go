package storage

import (
	"context"
	"testing"
	"time"
)

// Retrying brings a failed, cancelled or deferred operation back to pending with
// its backoff cleared; a live pending, processing or completed operation is
// refused. This is the in-memory twin of the PostgreSQL table test.
func TestRetryOperationStates(t *testing.T) {
	ctx := context.Background()
	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Hour)
	cases := []struct {
		name    string
		status  OperationStatus
		next    *time.Time
		wantOK  bool
		wantErr error
	}{
		{name: "failed", status: StatusFailed, wantOK: true},
		{name: "cancelled", status: StatusCancelled, wantOK: true},
		{name: "deferred", status: StatusPending, next: &future, wantOK: true},
		{name: "pending not deferred", status: StatusPending, wantErr: ErrOperationConflict},
		{name: "pending already due", status: StatusPending, next: &past, wantErr: ErrOperationConflict},
		{name: "processing", status: StatusProcessing, wantErr: ErrOperationConflict},
		{name: "completed", status: StatusCompleted, wantErr: ErrOperationConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryStore("public")
			if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
				t.Fatal(err)
			}
			op, err := store.PutOperation(ctx, Operation{
				OperationID: newID(), BankID: "b1", OperationType: OperationTypeConsolidate,
				Status: tc.status, Error: "boom", NextRetryAt: tc.next,
				Payload: map[string]any{"k": "v"},
			})
			if err != nil {
				t.Fatal(err)
			}
			retried, err := store.RetryOperation(ctx, "b1", op.OperationID)
			if !tc.wantOK {
				if err != tc.wantErr {
					t.Fatalf("retry = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if retried.Status != StatusPending || retried.NextRetryAt != nil || retried.Error != "" ||
				retried.RetryCount != 0 {
				t.Fatalf("retried = %+v, want pending with the backoff cleared", retried)
			}
		})
	}
}

// Retrying a deferred operation makes it claimable at once; a deferred operation
// is otherwise skipped by the claim until its next_retry_at.
func TestRetryOperationClearsTheBackoff(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	op, err := store.PutOperation(ctx, Operation{
		OperationID: newID(), BankID: "b1", OperationType: OperationTypeConsolidate,
		Status: StatusPending, Payload: map[string]any{"k": "v"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeferOperation(ctx, op.OperationID, time.Now().UTC().Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if claimable, err := store.ClaimNextOperation(ctx, "worker-1", time.Minute); err != ErrOperationNotFound {
		t.Fatalf("a deferred operation was claimable before it was due: %+v, %v", claimable, err)
	}
	if _, err := store.RetryOperation(ctx, "b1", op.OperationID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	claimed, err := store.ClaimNextOperation(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("a retried deferred operation was not claimable: %v", err)
	}
	if claimed.OperationID != op.OperationID || claimed.Status != StatusProcessing {
		t.Fatalf("claimed = %+v", claimed)
	}
}
