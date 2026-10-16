//go:build pgx

package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// pgLapsedCase queues and claims one operation in its own bank, runs a processor
// that deletes that bank mid-run, settles the outcome, and returns the captured
// log. It is the same shape as the in-memory cases, so the two backends are held
// to the same no-op rule.
func pgLapsedCase(t *testing.T, store storage.Store, bankID string, finish func(ctx context.Context, store storage.Store, bankID string) error) string {
	t.Helper()
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bankID, Name: bankID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID:   pgUUIDStr("lapsed-" + bankID),
		BankID:        bankID,
		OperationType: storage.OperationTypeTransferImport,
		Status:        storage.StatusPending,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	op, err := store.ClaimNextOperation(ctx, "worker-pg", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if op.BankID != bankID {
		t.Fatalf("claimed an operation of bank %q, want %q", op.BankID, bankID)
	}
	logger, logs := logCapture()
	consumer := New(store, ProcessorFunc(func(ctx context.Context, _ storage.Operation) error {
		return finish(ctx, store, bankID)
	}), Config{WorkerID: "worker-pg", Logger: logger, MaxRetries: 3})
	consumer.handle(ctx, op)
	return logs.String()
}

func assertPgLapsedNoop(t *testing.T, logs, action string) {
	t.Helper()
	if strings.Contains(logs, "level=ERROR") {
		t.Fatalf("a settle whose row is gone must not log an error:\n%s", logs)
	}
	if !strings.Contains(logs, action+" is a no-op") {
		t.Fatalf("the no-op must be reported as %q:\n%s", action, logs)
	}
}

// Each settle path finds its operation row removed by the bank-delete cascade
// and must treat that as the reference does: a write filtered on the running
// status that affects zero rows, silently.
func TestPostgresSettleIsNoopWhenOperationRowIsGone(t *testing.T) {
	store := postgresWorkerStore(t)

	t.Run("mark-completed", func(t *testing.T) {
		logs := pgLapsedCase(t, store, "lapsed-complete", func(ctx context.Context, store storage.Store, bankID string) error {
			_, err := store.DeleteBank(ctx, bankID)
			return err
		})
		assertPgLapsedNoop(t, logs, "mark-completed")
	})

	t.Run("mark-failed", func(t *testing.T) {
		logs := pgLapsedCase(t, store, "lapsed-failed", func(ctx context.Context, store storage.Store, bankID string) error {
			if _, err := store.DeleteBank(ctx, bankID); err != nil {
				return err
			}
			return Permanent(errors.New("boom"))
		})
		assertPgLapsedNoop(t, logs, "mark-failed")
	})

	t.Run("schedule-retry", func(t *testing.T) {
		logs := pgLapsedCase(t, store, "lapsed-retry", func(ctx context.Context, store storage.Store, bankID string) error {
			if _, err := store.DeleteBank(ctx, bankID); err != nil {
				return err
			}
			return errors.New("boom")
		})
		assertPgLapsedNoop(t, logs, "schedule-retry")
	})

	t.Run("defer", func(t *testing.T) {
		logs := pgLapsedCase(t, store, "lapsed-defer", func(ctx context.Context, store storage.Store, bankID string) error {
			if _, err := store.DeleteBank(ctx, bankID); err != nil {
				return err
			}
			return Defer(errors.New("rate limited"))
		})
		assertPgLapsedNoop(t, logs, "defer")
	})
}
