package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// logCapture is a logger whose output a test can read back.
func logCapture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// A claimed operation whose row is gone — deleted with its bank — must not run:
// executing it would resurrect the bank through the write path's lazy create.
// The guard reports the abort and the terminal write is a no-op, exactly as the
// reference's execution-start liveness check does.
func TestHandleSkipsOperationWhoseRowIsGone(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// The bank is deleted mid-flight: its queued operation row goes with it.
	if _, err := store.DeleteBank(ctx, "bank-a"); err != nil {
		t.Fatal(err)
	}

	var processed atomic.Int32
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		processed.Add(1)
		return nil
	}), Config{WorkerID: "worker-1"})
	consumer.handle(context.Background(), op)

	if processed.Load() != 0 {
		t.Fatal("an operation whose row is gone must not execute")
	}
}

// A claimed operation cancelled while in flight must abort at the guard rather
// than run to completion; cancelling is how a caller stops work already
// claimed.
func TestHandleSkipsCancelledOperation(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CancelOperation(ctx, "bank-a", "op-1"); err != nil {
		t.Fatal(err)
	}

	var processed atomic.Int32
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		processed.Add(1)
		return nil
	}), Config{WorkerID: "worker-1"})
	consumer.handle(context.Background(), op)

	if processed.Load() != 0 {
		t.Fatal("a cancelled operation must not execute")
	}
}

// A retry that the store already returned to the claimable pool must not be
// released a second time: the release finds the claim no longer held and fails
// with a conflict, which is noise on the log and, before the fix, the only
// error an operator could see. The recorded cause is the processor's, not a
// bookkeeping error.
func TestRetryDoesNotReleaseClaimAndKeepsTheRealCause(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	cause := errors.New("provider returned 503")
	logger, logs := logCapture()
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error {
		return cause
	}), Config{WorkerID: "worker-1", MaxRetries: 3, Logger: logger})
	consumer.handle(context.Background(), op)

	if strings.Contains(logs.String(), "claim release failed") {
		t.Fatalf("a scheduled retry must not attempt a second release:\n%s", logs.String())
	}
	after, err := store.GetOperation(ctx, "bank-a", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != storage.StatusPending {
		t.Fatalf("status = %s, want pending after a scheduled retry", after.Status)
	}
	if after.Error != cause.Error() {
		t.Fatalf("recorded error = %q, want the processor's cause %q", after.Error, cause.Error())
	}
	if after.WorkerID != "" {
		t.Fatalf("a retried operation must be released to the pool, worker = %q", after.WorkerID)
	}
}

// lapsedSettleCase claims one operation, runs a processor that deletes the
// operation's bank mid-run, settles the outcome, and returns the captured log.
// The operation is claimed first, so the guard at execution start passes; the
// bank then goes away while the work runs and the settle finds no row.
func lapsedSettleCase(t *testing.T, cfg Config, finish func(ctx context.Context, store *storage.MemoryStore) error) string {
	t.Helper()
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	logger, logs := logCapture()
	cfg.WorkerID = "worker-1"
	cfg.Logger = logger
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	consumer := New(store, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		return finish(ctx, store)
	}), cfg)
	consumer.handle(context.Background(), op)
	return logs.String()
}

// assertLapsedNoop fails when a lapsed settle logged an error or failed to
// report the no-op it is.
func assertLapsedNoop(t *testing.T, logs, action string) {
	t.Helper()
	if strings.Contains(logs, "level=ERROR") {
		t.Fatalf("a settle whose row is gone must not log an error:\n%s", logs)
	}
	if !strings.Contains(logs, action+" is a no-op") {
		t.Fatalf("the no-op must be reported as %q:\n%s", action, logs)
	}
}

// A completion that finds its operation row gone — the bank was deleted while
// the operation ran — is a no-op, not an ERROR: the reference's mark-completed
// is an UPDATE filtered on the running status and settles zero rows silently.
func TestCompleteIsNoopWhenOperationRowIsGone(t *testing.T) {
	store := newStore(t)
	queue(t, store, "op-1", storage.OperationTypeTransferImport)
	ctx := context.Background()
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	logger, logs := logCapture()
	consumer := New(store, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		_, err := store.DeleteBank(ctx, "bank-a")
		return err
	}), Config{WorkerID: "worker-1", Logger: logger})
	consumer.handle(context.Background(), op)

	assertLapsedNoop(t, logs.String(), "mark-completed")
}

// The same holds for a permanent failure.
func TestFailIsNoopWhenOperationRowIsGone(t *testing.T) {
	logs := lapsedSettleCase(t, Config{}, func(ctx context.Context, store *storage.MemoryStore) error {
		if _, err := store.DeleteBank(ctx, "bank-a"); err != nil {
			return err
		}
		return Permanent(errors.New("boom"))
	})
	assertLapsedNoop(t, logs, "mark-failed")
}

// And for a retry.
func TestScheduleRetryIsNoopWhenOperationRowIsGone(t *testing.T) {
	logs := lapsedSettleCase(t, Config{}, func(ctx context.Context, store *storage.MemoryStore) error {
		if _, err := store.DeleteBank(ctx, "bank-a"); err != nil {
			return err
		}
		return errors.New("boom")
	})
	assertLapsedNoop(t, logs, "schedule-retry")
}

// And for a deferral.
func TestDeferIsNoopWhenOperationRowIsGone(t *testing.T) {
	logs := lapsedSettleCase(t, Config{}, func(ctx context.Context, store *storage.MemoryStore) error {
		if _, err := store.DeleteBank(ctx, "bank-a"); err != nil {
			return err
		}
		return Defer(errors.New("rate limited"))
	})
	assertLapsedNoop(t, logs, "defer")
}

// A settle whose row is gone must not drive the parent-batch rollup: with the
// bank deleted, the parent is gone too, and rolling it up would only log a
// failure for work that no longer exists.
func TestLapsedCompleteSkipsParentRollup(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "parent-1", BankID: "bank-a", OperationType: storage.AsyncRetainType,
		Status: storage.StatusPending, Run: 1,
		ResultMetadata: map[string]any{"is_parent": true},
		CreatedAt:      time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-1", BankID: "bank-a", OperationType: storage.OperationTypeRetain,
		Status: storage.StatusPending, Run: 1,
		ResultMetadata: map[string]any{"parent_operation_id": "parent-1"},
		CreatedAt:      time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	op, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationID != "op-1" {
		t.Fatalf("claimed %q, want the child op-1", op.OperationID)
	}
	logger, logs := logCapture()
	consumer := New(store, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		_, err := store.DeleteBank(ctx, "bank-a")
		return err
	}), Config{WorkerID: "worker-1", Logger: logger})
	consumer.handle(context.Background(), op)

	if strings.Contains(logs.String(), "parent roll-up failed") {
		t.Fatalf("a lapsed settle must not attempt the parent rollup:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "batch parent promoted") {
		t.Fatalf("a gone parent must not be promoted:\n%s", logs.String())
	}
	assertLapsedNoop(t, logs.String(), "mark-completed")
}
