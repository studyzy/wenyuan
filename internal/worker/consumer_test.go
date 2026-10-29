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

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newStore builds an in-memory store with one bank, so an operation can be
// recorded against a real owner.
func newStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "bank-a", Name: "bank-a"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// queue records a pending operation.
func queue(t *testing.T, store storage.Store, id, opType string) storage.Operation {
	t.Helper()
	op, err := store.PutOperation(context.Background(), storage.Operation{
		OperationID:   id,
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
	final, err := store.GetOperation(context.Background(), "bank-a", "op-1")
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
	final, err := store.GetOperation(context.Background(), "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if final.WorkerID == "w1" {
		t.Fatalf("stopped consumer still holds the claim: %+v", final)
	}
}

// test-last-poll-stamp: the liveness payload reports the age of the last
// completed claim cycle, so a consumer that never ran reports nil and every
// completed cycle — including an empty batch — advances the stamp.
func TestLastPollStampsCompletedClaimCycles(t *testing.T) {
	store := newStore(t)
	consumer := New(store, stubProcessor{}, Config{WorkerID: "w1", Concurrency: 1, PollInterval: time.Millisecond, Logger: quietLogger()})
	if consumer.LastPoll() != nil {
		t.Fatal("a consumer that has not run must report no poll yet")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = consumer.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && consumer.LastPoll() == nil {
		time.Sleep(2 * time.Millisecond)
	}
	last := consumer.LastPoll()
	if last == nil {
		t.Fatal("a completed empty claim cycle must stamp the poll time")
	}
	if age := time.Since(*last); age > time.Second {
		t.Fatalf("poll stamp is stale: %v", age)
	}
	cancel()
	<-done
}

// waitForStatus polls until the operation reaches the wanted status.
func waitForStatus(t *testing.T, store storage.Store, id string, want storage.OperationStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		op, err := store.GetOperation(context.Background(), "bank-a", id)
		if err == nil && op.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	op, _ := store.GetOperation(context.Background(), "bank-a", id)
	t.Fatalf("operation %s status = %s, want %s", id, op.Status, want)
}

// stubProcessor returns a scripted error.
type stubProcessor struct{ err error }

func (s stubProcessor) Process(context.Context, storage.Operation) error { return s.err }

// Store backpressure defers the operation without consuming the retry budget,
// so a sustained shed cannot walk work toward permanent failure.
func TestBackpressureDefersWithoutBudget(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, stubProcessor{err: storage.ErrWritesUnavailable}, Config{
		WorkerID: "worker-1", MaxRetries: 3, InitialBackoff: time.Second,
		MaxBackoff: time.Minute, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now: func() time.Time { return time.Now().UTC() }, Sleep: func(time.Duration) {},
	})
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.RetryCount != 0 {
		t.Fatalf("backpressure must not consume the retry budget: %+v", after)
	}
	if after.Status != storage.StatusPending {
		t.Fatalf("a deferred operation must return to the pool: %+v", after)
	}
}

// blockingProcessor waits for the work context and reports its cancellation.
type blockingProcessor struct{}

func (blockingProcessor) Process(ctx context.Context, _ storage.Operation) error {
	<-ctx.Done()
	return ctx.Err()
}

// A per-type wall ceiling bounds a wedged task below the lease, so the slot is
// freed and the operation is retried.
func TestWallCeilingBoundsATask(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, blockingProcessor{}, Config{
		WorkerID: "worker-1", MaxRetries: 3, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now:          func() time.Time { return time.Now().UTC() },
		Sleep:        func(time.Duration) {},
		WallTimeouts: map[string]time.Duration{storage.OperationTypeTransferImport: 10 * time.Millisecond},
	})
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.RetryCount != 1 {
		t.Fatalf("wal-capped task must be retried once, got %+v", after)
	}
}

// A bank with a backlog does not starve another bank: claims interleave by each
// bank's oldest pending work.
func TestClaimInterleavesBanks(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	base := time.Now().UTC()
	put := func(id, bank string, at time.Time) {
		if _, err := store.PutOperation(ctx, storage.Operation{
			OperationID: id, BankID: bank, OperationType: storage.OperationTypeTransferImport,
			Status: storage.StatusPending, CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("a1", "bank-a", base)
	put("b1", "bank-b", base.Add(time.Second))
	put("a2", "bank-a", base.Add(2*time.Second))

	var order []string
	for i := 0; i < 3; i++ {
		op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, op.OperationID)
	}
	want := []string{"a1", "b1", "a2"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("claim order = %v, want %v", order, want)
		}
	}
}

// A handler-requested deferral is not counted against the retry budget.
func TestDeferralDoesNotConsumeBudget(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, stubProcessor{err: Defer(errors.New("provider rate limited"))}, Config{
		WorkerID: "worker-1", MaxRetries: 3, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now: func() time.Time { return time.Now().UTC() }, Sleep: func(time.Duration) {},
	})
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.RetryCount != 0 || after.Status != storage.StatusPending {
		t.Fatalf("a deferral must not consume the budget: %+v", after)
	}
}

// An explicit deferral delay controls the next attempt time.
func TestExplicitDeferralDelay(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	delay := time.Hour
	consumer := New(store, stubProcessor{err: DeferAt(delay, errors.New("retry later"))}, Config{
		WorkerID: "worker-1", MaxRetries: 3, InitialBackoff: time.Millisecond,
		MaxBackoff: time.Millisecond, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now: func() time.Time { return time.Now().UTC() }, Sleep: func(time.Duration) {},
	})
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRetryAt == nil || time.Until(*after.NextRetryAt) < 30*time.Minute {
		t.Fatalf("explicit deferral delay not applied: %+v", after.NextRetryAt)
	}
}

// A reserved type is claimed ahead of older work while its floor is unmet, so
// a bank mid-bulk-ingest cannot starve consolidation.
func TestReservedSlotsPreferReservedType(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-import", storage.OperationTypeTransferImport)
	queue(t, store, "op-consolidate", storage.OperationTypeConsolidate)
	consumer := New(store, stubProcessor{}, Config{
		WorkerID: "worker-1", Concurrency: 1, Lease: time.Hour,
		ReservedSlots: map[string]int{storage.OperationTypeConsolidate: 1},
		Logger:        quietLogger(),
	})
	op, _, err := consumer.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationType != storage.OperationTypeConsolidate {
		t.Fatalf("claimed %q, want the reserved type first", op.OperationType)
	}
	// With the floor met, the next claim takes the remaining work.
	op2, _, err := consumer.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if op2.OperationType != storage.OperationTypeTransferImport {
		t.Fatalf("second claim = %q, want the unreserved work", op2.OperationType)
	}
}

// A processor's reported outcome is persisted on the operation record when the
// consumer settles it, so an operator can read what the work did rather than
// only whether it finished.
func TestCompletedOperationCarriesReportedOutcome(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeRefreshMentalModel)

	consumer := New(store, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		ReportOutcome(ctx, map[string]any{
			"outcome":        "content_written",
			"created_before": "2026-01-01T00:00:00Z",
		})
		return nil
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusCompleted)
	final, err := store.GetOperation(context.Background(), "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if final.ResultMetadata["outcome"] != "content_written" {
		t.Fatalf("result metadata = %+v, want the reported outcome", final.ResultMetadata)
	}
	if final.ResultMetadata["created_before"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("result metadata = %+v, want the reported window", final.ResultMetadata)
	}
}

// A refusal is reported in the same vocabulary as a success, so a failed
// operation carries its outcome and reason rather than only a prose detail.
func TestFailedOperationCarriesReportedOutcome(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeRefreshMentalModel)

	consumer := New(store, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		ReportOutcome(ctx, map[string]any{
			"outcome":        "refresh_failed_error",
			"failure_reason": "no_answer",
		})
		return Permanent(errors.New("the reflection produced no answer"))
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusFailed)
	final, err := store.GetOperation(context.Background(), "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if final.ResultMetadata["outcome"] != "refresh_failed_error" {
		t.Fatalf("result metadata = %+v, want the failure outcome", final.ResultMetadata)
	}
	if final.ResultMetadata["failure_reason"] != "no_answer" {
		t.Fatalf("result metadata = %+v, want the failure reason", final.ResultMetadata)
	}
	if final.Error == "" {
		t.Fatal("the prose detail must survive alongside the structured record")
	}
}

// A processor that reports nothing completes exactly as before: no metadata is
// invented for it.
func TestOperationWithoutReportedOutcomeCompletesAsBefore(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)

	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return nil
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusCompleted)
	final, err := store.GetOperation(context.Background(), "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(final.ResultMetadata) != 0 {
		t.Fatalf("a processor that reports nothing must record nothing: %+v", final.ResultMetadata)
	}
}

// storeWithoutResults hides the optional result capability, standing in for a
// backend that cannot record operation metadata.
type storeWithoutResults struct{ storage.Store }

// A store without the capability still completes the operation; the reported
// metadata is dropped rather than turning completion into a failure.
func TestStoreWithoutResultCapabilityStillCompletes(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeRefreshMentalModel)

	consumer := New(storeWithoutResults{Store: store}, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		ReportOutcome(ctx, map[string]any{"outcome": "content_written"})
		return nil
	}), Config{WorkerID: "w1", Concurrency: 1, PollInterval: 5 * time.Millisecond, Logger: quietLogger()})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = consumer.Run(ctx) }()

	waitForStatus(t, store, "op-1", storage.StatusCompleted)
	final, err := store.GetOperation(context.Background(), "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(final.ResultMetadata) != 0 {
		t.Fatalf("a store without the capability must not record metadata: %+v", final.ResultMetadata)
	}
}

// Every wall-ceiling key must be an operation type the consumer can actually
// claim, because the ceiling is looked up as `WallTimeouts[op.OperationType]`. A
// key that names something else is dead weight that reads as a ceiling: the
// reference keys its ceilings by task type for this reason, and a refresh - one
// reflect plus its write - shares reflect's budget so that a wedged refresh
// cannot hold its worker slot until restart.
func TestWallCeilingsAreKeyedByClaimableOperationTypes(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	claimable := map[string]bool{
		storage.OperationTypeConsolidate:        true,
		storage.OperationTypeRefreshMentalModel: true,
		storage.OperationTypeRetain:             true,
		storage.OperationTypeWebhookDelivery:    true,
		storage.OperationTypeDocumentExport:     true,
		storage.OperationTypeDocumentImport:     true,
		storage.OperationTypeTransferExport:     true,
		storage.OperationTypeTransferImport:     true,
		storage.OperationTypeClone:              true,
	}
	for key := range cfg.Worker.WallTimeouts {
		if !claimable[key] {
			t.Errorf("wall ceiling %q is not an operation type the consumer claims, so it bounds nothing", key)
		}
	}
	// The refresh ceiling is the one this guards: it used to be keyed "reflect".
	if cfg.Worker.WallTimeouts[storage.OperationTypeRefreshMentalModel] <= 0 {
		t.Error("a refresh must carry a wall ceiling, or a wedged one holds its slot")
	}
	// And the reference key that sets it must resolve through the same entry.
	resolved := config.FromEnv(func(key string) (string, bool) {
		if key == config.EnvReflectWallTimeout {
			return "120", true
		}
		return "", false
	})
	if got := resolved.Worker.WallTimeouts[storage.OperationTypeRefreshMentalModel]; got != 120*time.Second {
		t.Errorf("WENYUAN_API_REFLECT_WALL_TIMEOUT set the refresh ceiling to %v, want 2m", got)
	}
}

// test-worker-consolidation-bank-priority: with a priority list configured the
// consumer takes consolidation from its own pool, preferring the bank whose
// pattern carries the higher priority over older work elsewhere.
func TestConsumerClaimsConsolidationByConfiguredBankPriority(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-a", BankID: "bank-a", OperationType: storage.OperationTypeConsolidate, Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-b", BankID: "bank-b", OperationType: storage.OperationTypeConsolidate, Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error { return nil }), Config{
		WorkerID: "worker-1", Lease: time.Hour, Logger: quietLogger(),
		ReservedSlots:       map[string]int{storage.OperationTypeConsolidate: 1},
		ConsolidationByBank: "bank-b:10,*:1",
	})
	op, _, err := consumer.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if op.BankID != "bank-b" {
		t.Fatalf("claimed %s, want the higher-priority bank", op.BankID)
	}
}

// With no priority configured the consumer claims the oldest consolidation, so
// an unset key leaves the scheduling as it was.
func TestConsumerWithoutPriorityClaimsOldestConsolidation(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-a", BankID: "bank-a", OperationType: storage.OperationTypeConsolidate, Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-b", BankID: "bank-b", OperationType: storage.OperationTypeConsolidate, Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error { return nil }), Config{
		WorkerID: "worker-1", Lease: time.Hour, Logger: quietLogger(),
		ReservedSlots: map[string]int{storage.OperationTypeConsolidate: 1},
	})
	op, _, err := consumer.claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationID != "op-a" {
		t.Fatalf("claimed %s, want the oldest consolidation", op.OperationID)
	}
}

// ParseBankPriority reads the reference's list, keeping a wildcard key and
// dropping a malformed entry rather than failing the consumer.
func TestParseBankPriority(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]int
	}{
		{"blank", "", nil},
		{"single", "billing:5", map[string]int{"billing": 5}},
		{"pattern and catch-all", "shadow-*:9,*:2", map[string]int{"shadow-*": 9, "*": 2}},
		{"spaces and colons in the pattern", " tenant:a:3 ", map[string]int{"tenant:a": 3}},
		{"drops a malformed entry", "good:4,broken,empty:,zero:0", map[string]int{"good": 4}},
	}
	for _, tc := range cases {
		got := ParseBankPriority(tc.raw)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
			continue
		}
		for pattern, want := range tc.want {
			if got[pattern] != want {
				t.Errorf("%s: pattern %q = %d, want %d", tc.name, pattern, got[pattern], want)
			}
		}
	}
}
