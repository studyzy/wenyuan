package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// enqueueSubject is the payload key the tests deduplicate on, matching the one
// the refresh path uses.
const enqueueSubject = "mental_model_id"

func enqueueRequest(bankID, subject string, includeProcessing bool) EnqueueRequest {
	return EnqueueRequest{
		Operation: Operation{
			BankID:        bankID,
			OperationType: OperationTypeRefreshMentalModel,
			Payload:       map[string]any{enqueueSubject: subject},
		},
		DedupeByPayloadKey:       enqueueSubject,
		DedupeIncludesProcessing: includeProcessing,
	}
}

// A second enqueue for the same subject is refused while the first is pending,
// whatever the processing flag says: every queued copy of a refresh does the
// same work, so a second one only pays for it twice.
func TestEnqueueDeduplicatesPendingRegardlessOfFlag(t *testing.T) {
	for _, includeProcessing := range []bool{false, true} {
		store := NewMemoryStore("public")
		ctx := context.Background()
		first, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-1", includeProcessing))
		if err != nil {
			t.Fatal(err)
		}
		if first.Deduplicated {
			t.Fatal("the first enqueue must insert")
		}
		second, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-1", includeProcessing))
		if err != nil {
			t.Fatal(err)
		}
		if !second.Deduplicated {
			t.Errorf("includeProcessing=%v: a pending operation must suppress the insert", includeProcessing)
		}
		if second.Operation.OperationID != first.Operation.OperationID {
			t.Errorf("the surviving operation must be returned: got %s, want %s", second.Operation.OperationID, first.Operation.OperationID)
		}
	}
}

// A *running* operation suppresses only when the caller asked it to. An
// automatic trigger is content with a running refresh covering its intent; an
// explicit request may need a run of its own, because the running one could
// have read its inputs before the caller's edit.
func TestEnqueueProcessingDeduplicationIsCallerChoice(t *testing.T) {
	ctx := context.Background()

	store := NewMemoryStore("public")
	if _, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-1", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Minute); err != nil {
		t.Fatalf("claim: %v", err)
	}
	explicit, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-1", false))
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Deduplicated {
		t.Error("an explicit enqueue must not fold into a running refresh")
	}
	automatic, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-1", true))
	if err != nil {
		t.Fatal(err)
	}
	if !automatic.Deduplicated {
		t.Error("an automatic enqueue must fold into a running refresh")
	}
	// The operation returned is whichever is running, which is not necessarily
	// the one enqueued first: the explicit enqueue above inserted a second
	// operation, and the claim may have taken either.
	if automatic.Operation.Status != StatusProcessing {
		t.Errorf("the returned operation must be the running one, got status %q", automatic.Operation.Status)
	}
}

// Deduplication is scoped to the subject: a different record's work is not
// suppressed, and neither is a different bank's.
func TestEnqueueDeduplicatesOnlyTheSameSubject(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-1", true)); err != nil {
		t.Fatal(err)
	}
	other, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-2", true))
	if err != nil {
		t.Fatal(err)
	}
	if other.Deduplicated {
		t.Error("a different subject must not be suppressed")
	}
	otherBank, err := store.EnqueueOperation(ctx, enqueueRequest("b2", "mm-1", true))
	if err != nil {
		t.Fatal(err)
	}
	if otherBank.Deduplicated {
		t.Error("the same subject in another bank must not be suppressed")
	}
}

// With no subject key named there is nothing to deduplicate against, so every
// enqueue inserts.
func TestEnqueueWithoutSubjectKeyAlwaysInserts(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	req := EnqueueRequest{Operation: Operation{BankID: "b1", OperationType: OperationTypeConsolidate}}
	for i := 0; i < 2; i++ {
		result, err := store.EnqueueOperation(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if result.Deduplicated {
			t.Fatal("an enqueue with no subject key must not deduplicate")
		}
	}
}

// A settled operation does not suppress: the work it did is finished, so the
// next trigger needs its own run.
func TestEnqueueIgnoresSettledOperations(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	first, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-1", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdvanceOperation(ctx, first.Operation.OperationID, StatusCompleted, ""); err != nil {
		t.Fatal(err)
	}
	next, err := store.EnqueueOperation(ctx, enqueueRequest("b1", "mm-1", true))
	if err != nil {
		t.Fatal(err)
	}
	if next.Deduplicated {
		t.Error("a completed operation must not suppress a new one")
	}
}

// A claim does not start an operation whose serialization key another live
// claim holds, and does start it once that claim is released.
func TestClaimRespectsSerializationKey(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()

	first := enqueueRequest("b1", "mm-1", false).Operation
	first.SerializationKey = "mental_model:mm-1"
	if _, err := store.EnqueueOperation(ctx, EnqueueRequest{Operation: first}); err != nil {
		t.Fatal(err)
	}
	second := enqueueRequest("b1", "mm-1", false).Operation
	second.SerializationKey = "mental_model:mm-1"
	if _, err := store.EnqueueOperation(ctx, EnqueueRequest{Operation: second}); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimNextOperation(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-2", time.Minute); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("the second operation must wait for the key: err = %v", err)
	}

	if err := store.ReleaseOperationClaim(ctx, claimed.OperationID, "worker-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-2", time.Minute); err != nil {
		t.Fatalf("after release the key is free: %v", err)
	}
}

// An operation carrying no key is never held back by another that does.
func TestClaimIgnoresKeysForUnkeyedOperations(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()

	keyed := enqueueRequest("b1", "mm-1", false).Operation
	keyed.SerializationKey = "mental_model:mm-1"
	if _, err := store.EnqueueOperation(ctx, EnqueueRequest{Operation: keyed}); err != nil {
		t.Fatal(err)
	}
	unkeyed := enqueueRequest("b1", "mm-2", false).Operation
	if _, err := store.EnqueueOperation(ctx, EnqueueRequest{Operation: unkeyed}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Minute); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
}

// A claim past its lease is abandoned, so a consumer that died holding a key
// does not block that key forever.
func TestAbandonedClaimReleasesTheKey(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	op := enqueueRequest("b1", "mm-1", false).Operation
	op.SerializationKey = "mental_model:mm-1"
	if _, err := store.EnqueueOperation(ctx, EnqueueRequest{Operation: op}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextOperation(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Age the claim past its lease without releasing it.
	past := time.Now().UTC().Add(-2 * time.Minute)
	stored := store.operations[claimed.OperationID]
	stored.ClaimedAt = &past
	store.operations[claimed.OperationID] = stored

	if _, err := store.ClaimNextOperation(ctx, "worker-2", time.Minute); err != nil {
		t.Fatalf("an abandoned claim must not hold the key: %v", err)
	}
}
