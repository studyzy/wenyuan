package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// test-claim-payload-predicate: the reference claims only rows whose
// task_payload is present. A pending, non-parent operation inserted with an
// absent payload — a row the reference would leave pending forever — must not
// be claimed, while an ordinary enqueue (whose payload normalises to an empty
// object, the column's form of a nil map) stays claimable.
func TestClaimSkipsPayloadLessOperation(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	store.mu.Lock()
	store.operations["orphan"] = Operation{
		OperationID: "orphan", BankID: "b1", OperationType: OperationTypeRetain,
		Status: StatusPending, Run: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	store.mu.Unlock()

	if _, err := store.ClaimNextOperation(ctx, "w1", time.Hour); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("a payload-less operation must not be claimable, got %v", err)
	}

	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "real", BankID: "b1", OperationType: OperationTypeRetain, Status: StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	op, err := store.ClaimNextOperation(ctx, "w1", time.Hour)
	if err != nil || op.OperationID != "real" {
		t.Fatalf("an enqueued operation must be claimable, got %+v (err %v)", op, err)
	}
}

// test-claim-consolidation-payload-predicate: the priority claim carries the
// same payload predicate as the shared claim, so a consolidation row with an
// absent payload is not picked up there either.
func TestPriorityClaimSkipsPayloadLessConsolidation(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	store.mu.Lock()
	store.operations["orphan"] = Operation{
		OperationID: "orphan", BankID: "b1", OperationType: OperationTypeConsolidate,
		Status: StatusPending, Run: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	store.mu.Unlock()

	if _, err := store.ClaimConsolidationByPriority(ctx, "w1", time.Hour, map[string]int{"*": 1}); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("a payload-less consolidation must not be claimable, got %v", err)
	}
}

// test-recover-consolidations-fact-type: the reference's retry clears the
// failure marker only on source facts. An observation that carries one is left
// alone and does not join the returned count.
func TestRecoverConsolidationsSkipsNonSourceFacts(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	failedAt := time.Date(2026, 1, 4, 3, 4, 5, 0, time.UTC)
	store.mu.Lock()
	store.units = map[string]MemoryUnit{
		"world": {ID: "world", BankID: "b1", FactType: TypeWorld, ConsolidationFailedAt: &failedAt},
		"exp":   {ID: "exp", BankID: "b1", FactType: TypeExperience, ConsolidationFailedAt: &failedAt},
		"obs":   {ID: "obs", BankID: "b1", FactType: TypeObservation, ConsolidationFailedAt: &failedAt},
	}
	store.mu.Unlock()

	retried, err := store.RecoverConsolidations(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if retried != 2 {
		t.Fatalf("retried = %d, want 2 (world + experience only)", retried)
	}
	store.mu.RLock()
	obs := store.units["obs"]
	store.mu.RUnlock()
	if obs.ConsolidationFailedAt == nil {
		t.Fatal("an observation's failure marker must not be cleared")
	}
}

// test-recover-consolidations-no-enqueue: the retry runs the requeue UPDATE and
// returns the count, as the reference does; it must not queue a consolidation
// pass of its own. A following POST /consolidate then reports a fresh run
// rather than a deduplicated one.
func TestRecoverConsolidationsDoesNotEnqueuePass(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	failedAt := time.Date(2026, 1, 4, 3, 4, 5, 0, time.UTC)
	store.mu.Lock()
	store.units = map[string]MemoryUnit{
		"world": {ID: "world", BankID: "b1", FactType: TypeWorld, ConsolidationFailedAt: &failedAt},
	}
	store.mu.Unlock()

	if _, err := store.RecoverConsolidations(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOperationForTypes(ctx, "w1", time.Hour, []string{OperationTypeConsolidate}); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("recover must not queue a consolidation pass, got %v", err)
	}
}
