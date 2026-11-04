package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A lost append race backs off on the reference's own ladder —
// min(2.0*2^(n-1), 300) seconds, scaled by a [0.5, 1.5) jitter — not on the
// generic retry backoff. The reference spells it in
// `_append_conflict_defer_seconds` (worker/poller.py:62-71).
func TestAppendConflictBackoffLadder(t *testing.T) {
	cases := []struct {
		deferCount int
		want       time.Duration
	}{
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, 64 * time.Second},
		{8, 256 * time.Second},
		// 2*2^(n-1) for n=9 is 512s, past the 300s ceiling.
		{9, AppendConflictDeferMax},
		{20, AppendConflictDeferMax},
		// A zero or negative count is read as the first deferral.
		{0, 2 * time.Second},
	}
	for _, tc := range cases {
		if got := appendConflictDeferSeconds(tc.deferCount, 1.0); got != tc.want {
			t.Errorf("defer #%d = %v, want %v", tc.deferCount, got, tc.want)
		}
	}
	// The jitter is the reference's random.uniform(0.5, 1.5): applied to the
	// ladder value, so the same deferral can land anywhere in [base/2, base*1.5).
	if got := appendConflictDeferSeconds(2, 0.5); got != 2*time.Second {
		t.Errorf("jitter 0.5 of the 4s rung = %v, want 2s", got)
	}
	if got := appendConflictDeferSeconds(2, 1.5); got != 6*time.Second {
		t.Errorf("jitter 1.5 of the 4s rung = %v, want 6s", got)
	}
}

// An append conflict is deferred on that ladder and records its deferral count
// on the operation, so a deferral that keeps repeating stays visible
// (worker/poller.py:1133-1160, `_APPEND_CONFLICT_DEFER_COUNT_KEY`).
//
// Falsification: drop the append-conflict branch from `deferOperation` and this
// FAILs — the delay becomes the ordinary backoff (an hour here) and no
// `append_conflict_defers` key is written.
func TestAppendConflictDeferReadsTheRecordedCountAndBumpsIt(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID:   "op-1",
		BankID:        "bank-a",
		OperationType: storage.OperationTypeRetain,
		Status:        storage.StatusPending,
		Payload:       map[string]any{"contents": []any{}},
		// Two earlier append-conflict deferrals already recorded, as the
		// reference stores them.
		ResultMetadata: map[string]any{AppendConflictDeferCountKey: float64(2)},
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return DeferAppendConflict(errors.New("document changed since it was read"))
	}), Config{
		Logger: quietLogger(), Lease: time.Hour, Concurrency: 1,
		// An hour of backoff would make the ladder invisible if the
		// append-conflict branch were missing.
		InitialBackoff: time.Hour, MaxBackoff: time.Hour,
		Now:    func() time.Time { return time.Now().UTC() },
		Jitter: func() float64 { return 1.0 },
	})
	before := time.Now().UTC()
	consumer.handle(ctx, op)

	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != storage.StatusPending {
		t.Fatalf("status = %s, want pending", after.Status)
	}
	// Third deferral: 2 * 2^(3-1) = 8s, jitter pinned to 1.0.
	if after.NextRetryAt == nil {
		t.Fatal("the deferral must be scheduled")
	}
	if delay := after.NextRetryAt.Sub(before); delay < 7*time.Second || delay > 9*time.Second {
		t.Fatalf("append-conflict deferral = %v, want the 8s third rung", delay)
	}
	// A deferral is not a failure: the budget is untouched and the error column
	// stays as it was.
	if after.RetryCount != 0 {
		t.Fatalf("retry count = %d, want 0 (a deferral spends no attempt)", after.RetryCount)
	}
	if after.Error != "" {
		t.Fatalf("error = %q, want empty (a deferral records no failure)", after.Error)
	}
	// The count is bumped and carried on the operation.
	if got := appendConflictDefers(after); got != 3 {
		t.Fatalf("%s = %d, want 3", AppendConflictDeferCountKey, got)
	}
}

// A store that cannot carry reported metadata still defers the append conflict
// on the ladder: the count is simply not recorded.
func TestAppendConflictDeferWithoutResultCapability(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	queue(t, store, "op-1", storage.OperationTypeRetain)
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(storeWithoutResults{Store: store}, ProcessorFunc(func(context.Context, storage.Operation) error {
		return DeferAppendConflict(errors.New("document changed"))
	}), Config{
		Logger: quietLogger(), Lease: time.Hour, Concurrency: 1,
		InitialBackoff: time.Hour, MaxBackoff: time.Hour,
		Now:    func() time.Time { return time.Now().UTC() },
		Jitter: func() float64 { return 1.0 },
	})
	before := time.Now().UTC()
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRetryAt == nil {
		t.Fatal("the deferral must still be scheduled")
	}
	if delay := after.NextRetryAt.Sub(before); delay < 1*time.Second || delay > 3*time.Second {
		t.Fatalf("first-rung deferral = %v, want ~2s", delay)
	}
}
