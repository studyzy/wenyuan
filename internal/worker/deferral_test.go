package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A processor that reports before asking for a deferral keeps its report on the
// operation across the retry, so a webhook's receiver status — the difference
// between "the endpoint is refusing us" and "we never reached it" — is not lost
// the moment the delivery is deferred.
func TestDeferredOperationCarriesReportedOutcome(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		ReportOutcome(ctx, map[string]any{"last_status_code": 429})
		return Defer(errors.New("receiver rate limited"))
	}), Config{
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
	if after.ResultMetadata["last_status_code"] != 429 {
		t.Fatalf("deferred operation lost its reported outcome: %+v", after.ResultMetadata)
	}
	// The deferral's own semantics are untouched: no budget spent, still pending.
	if after.RetryCount != 0 || after.Status != storage.StatusPending {
		t.Fatalf("a deferral must not consume the budget: %+v", after)
	}
}

// A deferral for a processor that reports nothing leaves the record exactly as
// it was: the richer path only ever adds a record the processor actually made.
func TestDeferredOperationWithoutReportRecordsNothing(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return Defer(errors.New("try again later"))
	}), Config{
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
	if len(after.ResultMetadata) != 0 {
		t.Fatalf("a processor that reports nothing must record nothing: %+v", after.ResultMetadata)
	}
	if after.Status != storage.StatusPending {
		t.Fatalf("the deferral must still be scheduled: %+v", after)
	}
}

// A store without the retry-result capability still defers the work; the report
// is dropped rather than turning the deferral into a failure.
func TestStoreWithoutRetryResultCapabilityStillDefers(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeRefreshMentalModel)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(storeWithoutResults{Store: store}, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		ReportOutcome(ctx, map[string]any{"last_status_code": 503})
		return Defer(errors.New("receiver unavailable"))
	}), Config{
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
	if after.Status != storage.StatusPending {
		t.Fatalf("a store without the capability must still defer: %+v", after)
	}
	if len(after.ResultMetadata) != 0 {
		t.Fatalf("a store without the capability must not record metadata: %+v", after.ResultMetadata)
	}
}

// test-backpressure-defer-seconds: a task the store shed under backpressure is
// held for the configured deferral interval, not for the retry backoff — the
// delay is how long the store needs, not how many attempts the work has spent.
func TestBackpressureDeferralUsesConfiguredDelay(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return storage.ErrWritesUnavailable
	}), Config{Logger: quietLogger(), Lease: time.Hour, BackpressureDefer: 90 * time.Second})
	before := time.Now().UTC()
	consumer.handle(ctx, op)

	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != storage.StatusPending || after.NextRetryAt == nil {
		t.Fatalf("a shed task must be rescheduled: %+v", after)
	}
	if delay := after.NextRetryAt.Sub(before); delay < 85*time.Second || delay > 95*time.Second {
		t.Fatalf("backpressure deferral = %v, want the configured 90s", delay)
	}
}

// An unset deferral interval falls back to the reference's default, so a shed
// task is not retried on the failure ladder's cadence.
func TestBackpressureDeferralDefaultsToReferenceInterval(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return storage.ErrWritesUnavailable
	}), Config{Logger: quietLogger(), Lease: time.Hour})
	before := time.Now().UTC()
	consumer.handle(ctx, op)

	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRetryAt == nil {
		t.Fatalf("a shed task must be rescheduled: %+v", after)
	}
	if delay := after.NextRetryAt.Sub(before); delay < DefaultBackpressureDefer-time.Second || delay > DefaultBackpressureDefer+time.Second {
		t.Fatalf("default backpressure deferral = %v, want %v", delay, DefaultBackpressureDefer)
	}
}

// A deferral that named its own delay keeps it: a webhook's backoff ladder is
// not overridden by the backpressure interval.
func TestExplicitDeferralDelayWinsOverBackpressureInterval(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return DeferAt(5*time.Second, errors.New("try later"))
	}), Config{Logger: quietLogger(), Lease: time.Hour, BackpressureDefer: 90 * time.Second})
	before := time.Now().UTC()
	consumer.handle(ctx, op)

	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRetryAt == nil {
		t.Fatalf("the deferral must still be scheduled: %+v", after)
	}
	if delay := after.NextRetryAt.Sub(before); delay < 4*time.Second || delay > 6*time.Second {
		t.Fatalf("explicit deferral = %v, want the named 5s", delay)
	}
}

// A deferral that is not backpressure — a provider rate limit, say — keeps the
// ordinary backoff: the configured interval is specific to a store that shed
// load.
func TestNonBackpressureDeferralKeepsBackoff(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return Defer(errors.New("provider rate limited"))
	}), Config{
		Logger: quietLogger(), Lease: time.Hour, BackpressureDefer: 90 * time.Second,
		InitialBackoff: 3 * time.Second, MaxBackoff: time.Minute,
	})
	before := time.Now().UTC()
	consumer.handle(ctx, op)

	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.NextRetryAt == nil {
		t.Fatalf("the deferral must still be scheduled: %+v", after)
	}
	if delay := after.NextRetryAt.Sub(before); delay < 2*time.Second || delay > 4*time.Second {
		t.Fatalf("non-backpressure deferral = %v, want the 3s backoff", delay)
	}
}

// A retry that names its own delay spends an attempt: the count advances and the
// cause is recorded, so a caller's ladder walks to its own ceiling instead of
// re-running the first rung forever. The consumer's own retry budget does not
// apply — the delay, and therefore the ceiling, is the caller's.
func TestRetryAtSpendsAnAttemptAtTheNamedDelay(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// MaxRetries is below the number of attempts this processor will make, so a
	// budget-governed retry would stop at the first one.
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return RetryAt(5*time.Second, errors.New("HTTP 404 from https://example.test/hook: nope"))
	}), Config{
		WorkerID: "worker-1", MaxRetries: 1, InitialBackoff: time.Hour,
		MaxBackoff: time.Hour, Lease: time.Hour, Concurrency: 1,
		PollInterval: time.Millisecond, Logger: quietLogger(),
		Now: func() time.Time { return time.Now().UTC() }, Sleep: func(time.Duration) {},
	})
	consumer.handle(ctx, op)
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.RetryCount != 1 {
		t.Fatalf("retry count = %d, want 1 (the attempt must be spent)", after.RetryCount)
	}
	if after.Status != storage.StatusPending {
		t.Fatalf("status = %s, want pending", after.Status)
	}
	if after.Error != "HTTP 404 from https://example.test/hook: nope" {
		t.Fatalf("recorded cause = %q, want the processor's message", after.Error)
	}
	if after.NextRetryAt == nil {
		t.Fatal("the retry must be scheduled")
	}
	// The named delay, not the consumer's backoff, is what was scheduled.
	if delay := time.Until(*after.NextRetryAt); delay > 30*time.Second {
		t.Fatalf("next retry in %s, want the named 5s delay rather than the 1h backoff", delay)
	}
}
