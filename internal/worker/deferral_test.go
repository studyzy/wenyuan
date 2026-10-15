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
