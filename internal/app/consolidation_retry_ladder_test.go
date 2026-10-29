package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/worker"
)

// The consolidation retry ladder is its own: capped exponential backoff
// starting at five seconds and topping out at thirty minutes
// (engine/memory_engine.py:529-533).
func TestConsolidationRetryBackoffLadder(t *testing.T) {
	tests := []struct {
		retryCount int
		want       time.Duration
	}{
		{0, 5 * time.Second},
		{1, 10 * time.Second},
		{2, 20 * time.Second},
		{3, 40 * time.Second},
		{4, 80 * time.Second},
		{5, 160 * time.Second},
		{6, 320 * time.Second},
		{7, 640 * time.Second},
		{8, 1280 * time.Second},
		{9, 1800 * time.Second},
		{10, 1800 * time.Second},
		{50, 1800 * time.Second},
	}
	for _, test := range tests {
		if got := consolidationRetryBackoff(test.retryCount); got != test.want {
			t.Fatalf("consolidationRetryBackoff(%d) = %s, want %s", test.retryCount, got, test.want)
		}
	}
}

// A transient consolidation failure retries on the ladder instead of walking
// the generic budget: an exhausted budget would dead-letter the bank's backlog
// after a few attempts (engine/memory_engine.py:5185-5205).
func TestFailedConsolidationRetriesBeyondTheBudget(t *testing.T) {
	ctx := context.Background()
	store := brokenFactsStore{MemoryStore: newConsolidationBankStore(t)}
	op, err := store.PutOperation(ctx, storage.Operation{
		BankID: "b1", OperationType: storage.OperationTypeConsolidate,
		Payload: map[string]any{"bank_id": "b1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A budget of zero would fail the operation on its first attempt were it
	// not exempt: the ladder schedules it twice instead.
	consumer := worker.New(store, archiveProcessor{store: store, provider: &scriptedProvider{}}, worker.Config{
		WorkerID: "worker-1", MaxRetries: 0, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sleep: func(time.Duration) {},
	})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = consumer.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()

	base := time.Now().UTC().Add(-time.Second)
	deadline := time.Now().UTC().Add(30 * time.Second)
	for {
		got, err := store.GetOperation(ctx, "b1", op.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == storage.StatusFailed {
			t.Fatalf("operation failed after %d attempts, want the ladder to keep it queued", got.RetryCount)
		}
		if got.RetryCount >= 2 {
			if got.NextRetryAt == nil || got.NextRetryAt.Before(base.Add(4*time.Second)) {
				t.Fatalf("retry count %d at %v, want the ladder's schedule past the first rung", got.RetryCount, got.NextRetryAt)
			}
			return
		}
		if time.Now().UTC().After(deadline) {
			t.Fatalf("the operation never climbed the ladder: %+v", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// When another consolidation is already pending for the bank, the retry is
// skipped and the failed pass terminates now: the pending op will process the
// same unconsolidated rows when it runs (engine/memory_engine.py:5165-5183).
func TestFailedConsolidationWithPendingPeerFailsNow(t *testing.T) {
	ctx := context.Background()
	store := brokenFactsStore{MemoryStore: newConsolidationBankStore(t)}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-peer", BankID: "b1", OperationType: storage.OperationTypeConsolidate,
		Status: storage.StatusPending, Payload: map[string]any{"bank_id": "b1"},
	}); err != nil {
		t.Fatal(err)
	}
	op := storage.Operation{OperationID: "op-c", BankID: "b1", OperationType: storage.OperationTypeConsolidate}
	err := archiveProcessor{store: store, provider: &scriptedProvider{}}.Process(ctx, op)
	if err == nil {
		t.Fatal("a pass whose store read failed must fail")
	}
	var permanent *worker.PermanentError
	if !errors.As(err, &permanent) {
		t.Fatalf("error = %v, want the retry skipped and the pass failed terminally", err)
	}
	if !strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("error = %q, want the pass's own cause", err.Error())
	}
}

// A wall-clock cancellation is not a transient pass failure: the decision is
// left to the consumer's own handling, exactly as before.
func TestConsolidationRetryLeavesWallClockCancellationAlone(t *testing.T) {
	p := archiveProcessor{}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		if retry, ok := p.consolidationRetry(context.Background(), storage.Operation{
			OperationType: storage.OperationTypeConsolidate,
		}, cause); ok {
			t.Fatalf("cause %v produced %v, want no consolidation decision", cause, retry)
		}
	}
}

// newConsolidationBankStore returns a memory store holding the bank a queued
// consolidation pass runs over.
func newConsolidationBankStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	return store
}
