package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// WithReclaimLimit makes lease-expiry recovery count: claiming an abandoned
// 'processing' row spends one of its recovery attempts, and a row whose budget
// is spent is failed with the reference's own sentence rather than run again
// (worker/poller.py:1496-1565).
func TestClaimReclaimsCountTowardTheRecoveryBudget(t *testing.T) {
	ctx := context.Background()
	const limit = 3
	past := time.Now().UTC().Add(-2 * time.Hour)
	tests := []struct {
		name          string
		retryCount    int
		wantClaimed   bool
		wantRetries   int // the retry count the claimed row must carry
		wantFailedNow bool
	}{
		{
			name:        "an expired claim is recovered and counted",
			retryCount:  0,
			wantClaimed: true,
			wantRetries: 1,
		},
		{
			name:        "the last remaining attempt is still recovered",
			retryCount:  2,
			wantClaimed: true,
			wantRetries: 3,
		},
		{
			name:          "a spent budget fails the row instead of running it",
			retryCount:    3,
			wantFailedNow: true,
		},
		{
			name:          "a spent budget beyond the cap fails the row too",
			retryCount:    5,
			wantFailedNow: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore("public")
			if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
				t.Fatal(err)
			}
			claimedAt := past
			op, err := store.PutOperation(ctx, Operation{
				OperationID: newID(), BankID: "b1", OperationType: OperationTypeConsolidate,
				Status: StatusProcessing, Payload: map[string]any{"k": "v"},
				WorkerID: "worker-0", ClaimedAt: &claimedAt, RetryCount: test.retryCount,
			})
			if err != nil {
				t.Fatal(err)
			}
			claimed, claimErr := store.ClaimNextOperation(ctx, "worker-1", time.Hour, WithReclaimLimit(limit))
			if test.wantFailedNow {
				if !errors.Is(claimErr, ErrOperationNotFound) {
					t.Fatalf("claim = %v, want the row failed and nothing claimable", claimErr)
				}
				after, err := store.GetOperation(ctx, "b1", op.OperationID)
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
				if after.WorkerID != "" || after.ClaimedAt != nil {
					t.Fatalf("the failed row must not keep its claim: %+v", after)
				}
				return
			}
			if claimErr != nil {
				t.Fatal(claimErr)
			}
			if !test.wantClaimed || claimed.OperationID != op.OperationID {
				t.Fatalf("claimed = %+v, want the abandoned row", claimed)
			}
			if claimed.RetryCount != test.wantRetries {
				t.Fatalf("retry_count = %d, want %d after the recovery", claimed.RetryCount, test.wantRetries)
			}
			if claimed.WorkerID != "worker-1" {
				t.Fatalf("worker_id = %q, want the new claim's holder", claimed.WorkerID)
			}
		})
	}
}

// Without WithReclaimLimit a claim makes no recovery counting: the abandoned
// row is claimed as before and a spent budget fails nothing.
func TestClaimWithoutReclaimLimitDoesNotCount(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Now().UTC().Add(-2 * time.Hour)
	op, err := store.PutOperation(ctx, Operation{
		OperationID: newID(), BankID: "b1", OperationType: OperationTypeConsolidate,
		Status: StatusProcessing, Payload: map[string]any{"k": "v"},
		WorkerID: "worker-0", ClaimedAt: &claimedAt, RetryCount: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.OperationID != op.OperationID || claimed.RetryCount != 7 {
		t.Fatalf("claimed = %+v, want the abandoned row without spending", claimed)
	}
	if claimed.Status != StatusProcessing {
		t.Fatalf("status = %s, want it claimed", claimed.Status)
	}
}

// A fresh pending row is claimed without spending anything, and an at-cap row
// that is NOT abandoned (its lease still runs) is left alone.
func TestRecoveryCountingTouchesOnlyAbandonedClaims(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := store.PutOperation(ctx, Operation{
		OperationID: newID(), BankID: "b1", OperationType: OperationTypeConsolidate,
		Status: StatusPending, Payload: map[string]any{"k": "v"}, RetryCount: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour, WithReclaimLimit(3))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.OperationID != fresh.OperationID || claimed.RetryCount != 9 {
		t.Fatalf("claimed = %+v, want the pending row claimed without spending", claimed)
	}

	// A live claim inside its lease is not abandoned, however many attempts it
	// has spent: the recovery must not fail it under the worker's feet.
	running := time.Now().UTC().Add(-time.Minute)
	live, err := store.PutOperation(ctx, Operation{
		OperationID: newID(), BankID: "b1", OperationType: OperationTypeRetain,
		Status: StatusProcessing, Payload: map[string]any{"k": "v"},
		WorkerID: "worker-2", ClaimedAt: &running, RetryCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour, WithReclaimLimit(3)); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("claim = %v, want the live claim left alone", err)
	}
	after, err := store.GetOperation(ctx, "b1", live.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusProcessing || after.Error != "" {
		t.Fatalf("live claim = %+v, want it untouched by the recovery", after)
	}
}

// A reclaimed batch child whose budget is spent fails and terminalises its
// parent, exactly as the reference propagates its reclaimed children
// (worker/poller.py:1548-1556).
func TestRecoveryFailureRollsUpTheBatchParent(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
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
	// Abandon the child mid-run with its budget already spent.
	child := children[0]
	full, err := store.GetOperation(ctx, "b1", child.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: child.OperationID, BankID: "b1", OperationType: OperationTypeRetain,
		Status: StatusProcessing, Payload: full.Payload,
		WorkerID: "worker-0", ClaimedAt: &expired, RetryCount: 3,
		ResultMetadata: full.ResultMetadata,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour, WithReclaimLimit(3)); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("claim = %v, want the spent child failed and nothing claimable", err)
	}
	parent, err := store.GetOperation(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Status != StatusFailed {
		t.Fatalf("parent = %s, want it promoted by the reclaim", parent.Status)
	}
	if parent.Error != reclaimExhaustedMessage(3) {
		t.Fatalf("parent error = %q, want the child's own cause", parent.Error)
	}
	if parent.CompletedAt != nil {
		t.Fatalf("parent completed_at = %v, want it unset for a failed batch", *parent.CompletedAt)
	}
}
