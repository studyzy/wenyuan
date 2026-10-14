//go:build pgx

package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// postgresWorkerStore opens a real PostgreSQL backend in its own schema. The
// test is skipped when no database URL is configured, so the default gate stays
// offline; it must be RUN explicitly rather than assumed.
func postgresWorkerStore(t *testing.T) storage.Store {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the PostgreSQL worker test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("worker_e2e_%d", time.Now().UnixNano())
	backend := &storage.PostgresBackend{}
	store, err := backend.Open(ctx, storage.BackendConfig{
		DatabaseURL:   url,
		Schema:        schema,
		RunMigrations: true,
	})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		// Drop the schema, not just the connection: every run migrates a
		// uniquely named schema, and leaving it behind accumulates until the
		// cross-schema discovery routines run out of lock table opening a
		// cursor per schema.
		if dropper, ok := store.(interface{ DropSchemaForTest(string) error }); ok {
			_ = dropper.DropSchemaForTest(schema)
		}
		if closer, ok := store.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})
	if _, err := store.CreateBank(ctx, storage.Bank{ID: "bank-a", Name: "bank-a"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	return store
}

// test-postgres-claim-is-atomic-under-contention: two consumers polling the
// same queue must never claim the same operation. This is the property the
// memory backend cannot prove, because it is enforced by the database's
// row locking rather than by in-process state.
func TestPostgresClaimIsAtomicUnderContention(t *testing.T) {
	store := postgresWorkerStore(t)
	ctx := context.Background()

	const operations = 20
	for i := 0; i < operations; i++ {
		if _, err := store.PutOperation(ctx, storage.Operation{
			ID:            fmt.Sprintf("op-%02d", i),
			BankID:        "bank-a",
			OperationType: storage.OperationTypeTransferImport,
			Status:        storage.StatusPending,
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Eight consumers race for the same twenty operations.
	const consumers = 8
	var mu sync.Mutex
	claimedBy := map[string]string{}
	var wg sync.WaitGroup
	for c := 0; c < consumers; c++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			workerID := fmt.Sprintf("worker-%d", id)
			for {
				op, err := store.ClaimNextOperation(ctx, workerID, time.Hour)
				if err != nil {
					if errors.Is(err, storage.ErrOperationNotFound) {
						return
					}
					return
				}
				mu.Lock()
				if previous, seen := claimedBy[op.ID]; seen {
					mu.Unlock()
					t.Errorf("operation %s was claimed twice: by %s then %s", op.ID, previous, workerID)
					return
				}
				claimedBy[op.ID] = workerID
				mu.Unlock()
			}
		}(c)
	}
	wg.Wait()

	if len(claimedBy) != operations {
		t.Fatalf("claimed %d operations, want %d", len(claimedBy), operations)
	}
	// Every operation must now be held, so a further claim finds nothing.
	if _, err := store.ClaimNextOperation(ctx, "late-worker", time.Hour); !errors.Is(err, storage.ErrOperationNotFound) {
		t.Fatalf("all work was claimed, so a further claim must find nothing, got %v", err)
	}
}

// test-postgres-claim-survives-a-live-lease and is reclaimable once the lease
// expires. A consumer that dies mid-operation must not strand its work.
func TestPostgresClaimLeaseExpiry(t *testing.T) {
	store := postgresWorkerStore(t)
	ctx := context.Background()

	if _, err := store.PutOperation(ctx, storage.Operation{
		ID: "op-lease", BankID: "bank-a",
		OperationType: storage.OperationTypeTransferImport,
		Status:        storage.StatusPending,
		CreatedAt:     time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// A live lease protects the claim.
	if _, err := store.ClaimNextOperation(ctx, "worker-2", time.Hour); !errors.Is(err, storage.ErrOperationNotFound) {
		t.Fatalf("a live claim must not be reclaimable, got %v", err)
	}
	// A zero-second lease marks any claim abandoned, which is how a dead
	// consumer's work is recovered.
	reclaimed, err := store.ClaimNextOperation(ctx, "worker-2", 0)
	if err != nil {
		t.Fatalf("an abandoned claim must be reclaimable: %v", err)
	}
	if reclaimed.WorkerID != "worker-2" {
		t.Fatalf("claim holder = %q, want worker-2", reclaimed.WorkerID)
	}
}

// test-postgres-end-to-end-drain: a real consumer drains a real queue against
// a real database, including a retry that succeeds on its second attempt.
func TestPostgresWorkerDrainsQueue(t *testing.T) {
	store := postgresWorkerStore(t)
	ctx := context.Background()

	for i := 0; i < 6; i++ {
		if _, err := store.PutOperation(ctx, storage.Operation{
			ID:            fmt.Sprintf("drain-%02d", i),
			BankID:        "bank-a",
			OperationType: storage.OperationTypeTransferImport,
			Status:        storage.StatusPending,
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// One operation fails once and then succeeds, so the retry path is
	// exercised against the database rather than only in memory.
	var mu sync.Mutex
	seen := map[string]int{}
	consumer := New(store, ProcessorFunc(func(_ context.Context, op storage.Operation) error {
		mu.Lock()
		seen[op.ID]++
		attempt := seen[op.ID]
		mu.Unlock()
		if op.ID == "drain-03" && attempt == 1 {
			return errors.New("transient on the first attempt")
		}
		return nil
	}), Config{
		WorkerID:       "drain-worker",
		Concurrency:    3,
		PollInterval:   20 * time.Millisecond,
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     50 * time.Millisecond,
		Lease:          30 * time.Second,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.Run(runCtx) }()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		page, err := store.ListOperations(ctx, storage.OperationQuery{
			BankID: "bank-a",
			Status: string(storage.StatusCompleted),
		})
		if err == nil && page.Total == 6 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "bank-a"})
	if err != nil {
		t.Fatal(err)
	}
	completed := 0
	for _, op := range page.Operations {
		if op.Status == storage.StatusCompleted {
			completed++
		}
		// A completed operation must not still look held.
		if op.Status.Terminal() && op.WorkerID != "" {
			t.Fatalf("terminal operation %s still holds a claim by %q", op.ID, op.WorkerID)
		}
	}
	if completed != 6 {
		t.Fatalf("completed %d operations, want 6", completed)
	}

	// The retried operation must have needed exactly two attempts.
	mu.Lock()
	retried := seen["drain-03"]
	mu.Unlock()
	if retried != 2 {
		t.Fatalf("the transient operation was attempted %d times, want 2", retried)
	}

	consumer.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not stop")
	}
}
