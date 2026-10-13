package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// newStore builds an in-memory store with one bank, so an operation can be
// recorded against a real owner.
func newStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{ID: "bank-a", Name: "bank-a"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// queue records a pending operation.
func queue(t *testing.T, store storage.Store, id, opType string) storage.Operation {
	t.Helper()
	op, err := store.PutOperation(context.Background(), storage.Operation{
		ID:            id,
		BankID:        "bank-a",
		OperationType: opType,
		Status:        storage.StatusPending,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// quietLogger discards consumer progress so test output stays readable.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// test-claim-is-exclusive: a claim moves the operation to processing and marks
// its holder, and a second claim finds nothing until the lease expires.
func TestClaimIsExclusive(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()

	first, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first.Status != storage.StatusProcessing || first.WorkerID != "worker-1" {
		t.Fatalf("claim did not mark the holder: %+v", first)
	}
	if first.ClaimedAt == nil {
		t.Fatal("claim must record when it was taken")
	}

	// A live lease protects the claim from another consumer.
	if _, err := store.ClaimNextOperation(ctx, "worker-2", time.Hour); !errors.Is(err, storage.ErrOperationNotFound) {
		t.Fatalf("a live claim must not be reclaimable, got %v", err)
	}
}

// test-expired-lease-is-reclaimable: a consumer that died mid-operation must
// not strand its work, so a claim older than the lease is reclaimable.
func TestExpiredLeaseIsReclaimable(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()

	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	reclaimed, err := store.ClaimNextOperation(ctx, "worker-2", time.Nanosecond)
	if err != nil {
		t.Fatalf("an expired claim must be reclaimable: %v", err)
	}
	if reclaimed.WorkerID != "worker-2" {
		t.Fatalf("reclaim did not transfer the claim: %+v", reclaimed)
	}
}

// test-release-returns-to-pool: releasing a claim makes the work claimable
// again immediately, which is what a graceful shutdown relies on.
func TestReleaseReturnsWorkToPool(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()

	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseOperationClaim(ctx, "op-1", "worker-1"); err != nil {
		t.Fatal(err)
	}
	again, err := store.ClaimNextOperation(ctx, "worker-2", time.Hour)
	if err != nil {
		t.Fatalf("released work must be claimable again: %v", err)
	}
	if again.WorkerID != "worker-2" {
		t.Fatalf("claim holder = %q", again.WorkerID)
	}
}

// test-release-requires-the-holder: one consumer must not release another's
// claim.
func TestReleaseRequiresHolder(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()

	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseOperationClaim(ctx, "op-1", "worker-2"); !errors.Is(err, storage.ErrOperationConflict) {
		t.Fatalf("a non-holder must not release, got %v", err)
	}
}

// test-successful-operation-completes: a processor that succeeds drives the
// operation to its terminal success state.
func TestSuccessfulOperationCompletes(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)

	var processed atomic.Int32
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		processed.Add(1)
		return nil
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusCompleted)
	if processed.Load() != 1 {
		t.Fatalf("operation processed %d times, want 1", processed.Load())
	}
}

// test-permanent-failure-does-not-retry: a permanent failure is terminal on the
// first try and must not consume the retry budget.
func TestPermanentFailureDoesNotRetry(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)

	var attempts atomic.Int32
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		attempts.Add(1)
		return Permanent(errors.New("input will never be accepted"))
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusFailed)
	if attempts.Load() != 1 {
		t.Fatalf("a permanent failure was attempted %d times, want 1", attempts.Load())
	}
}

// test-retry-then-exhaustion: a transient failure is retried up to the attempt
// budget and then fails with the budget recorded.
func TestRetryThenExhaustion(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)

	var attempts atomic.Int32
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		attempts.Add(1)
		return errors.New("transient")
	}), Config{
		WorkerID:       "w1",
		Concurrency:    1,
		PollInterval:   5 * time.Millisecond,
		MaxRetries:     2,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     time.Millisecond,
		Logger:         quietLogger(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusFailed)
	// One first try plus two retries.
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3 (one try plus two retries)", got)
	}
	final, err := store.GetOperation(context.Background(), "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if final.RetryCount != 2 {
		t.Fatalf("retry count = %d, want 2", final.RetryCount)
	}
	if final.Error == "" {
		t.Fatal("the exhausted failure must record why")
	}
}

// test-backoff-grows-and-is-clamped: the retry delay doubles from the initial
// value and never exceeds the ceiling.
func TestBackoffGrowsAndIsClamped(t *testing.T) {
	consumer := New(newStore(t), ProcessorFunc(nil), Config{
		InitialBackoff: time.Second,
		MaxBackoff:     8 * time.Second,
		Logger:         quietLogger(),
	})
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		// Past the ceiling the delay stops growing.
		{5, 8 * time.Second},
		{50, 8 * time.Second},
		// An out-of-contract attempt index is treated as the first retry.
		{0, time.Second},
	}
	for _, tc := range cases {
		if got := consumer.BackoffFor(tc.attempt); got != tc.want {
			t.Fatalf("backoff for attempt %d = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

// test-negative-budget-clamps-to-zero: a negative attempt budget means "no
// retry", never a negative count.
func TestNegativeBudgetClampsToZero(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)

	var attempts atomic.Int32
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		attempts.Add(1)
		return errors.New("transient")
	}), Config{
		WorkerID: "w1", Concurrency: 1, MaxRetries: -5,
		PollInterval: 5 * time.Millisecond, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, Logger: quietLogger(),
	})
	if consumer.cfg.MaxRetries != 0 {
		t.Fatalf("max retries = %d, want 0", consumer.cfg.MaxRetries)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusFailed)
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 with no retry budget", attempts.Load())
	}
}

// test-concurrency-bound-is-respected: the consumer never runs more operations
// at once than it is configured for.
func TestConcurrencyBoundIsRespected(t *testing.T) {
	store := newStore(t)
	for i := 0; i < 12; i++ {
		queue(t, store, "op-"+string(rune('a'+i)), storage.OperationTypeTransferImport)
	}

	var inFlight, peak atomic.Int32
	var mu sync.Mutex
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		current := inFlight.Add(1)
		mu.Lock()
		if current > peak.Load() {
			peak.Store(current)
		}
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return nil
	}), Config{WorkerID: "w1", Concurrency: 3, PollInterval: time.Millisecond, Logger: quietLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		page, err := store.ListOperations(context.Background(), storage.OperationQuery{Status: string(storage.StatusCompleted)})
		if err == nil && page.Total == 12 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A generous ceiling: the slot count must be the bound even if scheduling
	// spreads work unevenly.
	if peak.Load() > 3 {
		t.Fatalf("peak concurrency = %d, want at most 3", peak.Load())
	}
}

// test-graceful-stop-hands-back-held-work: on shutdown the consumer releases
// what it still holds instead of stranding it for the lease.
func TestGracefulStopReleasesHeldWork(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)

	block := make(chan struct{})
	consumer := New(store, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		<-block
		return nil
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: time.Millisecond, Logger: quietLogger()})

	runDone := make(chan struct{})
	go func() {
		_ = consumer.Run(context.Background())
		close(runDone)
	}()

	// Wait until the operation is actually claimed, then stop and unblock.
	waitForStatus(t, store, "op-1", storage.StatusProcessing)
	consumer.Stop()
	close(block)

	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("consumer did not stop")
	}

	// The work was either completed by the in-flight handler or handed back;
	// it must not still be held by the stopped consumer.
	final, err := store.GetOperation(context.Background(), "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if final.WorkerID == "w1" {
		t.Fatalf("stopped consumer still holds the claim: %+v", final)
	}
}

// waitForStatus polls until the operation reaches the wanted status.
func waitForStatus(t *testing.T, store storage.Store, id string, want storage.OperationStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		op, err := store.GetOperation(context.Background(), id)
		if err == nil && op.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	op, _ := store.GetOperation(context.Background(), id)
	t.Fatalf("operation %s status = %s, want %s", id, op.Status, want)
}
