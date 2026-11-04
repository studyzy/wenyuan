//go:build pgx

package storage

import (
	"context"
	"errors"
	"testing"
)

// A consolidation submit against a bank that does not exist is a clean
// not-found, not the opaque foreign-key 500 the INSERT would otherwise raise.
//
// The reference verifies the bank on every serialize path — a submit that
// deduplicates by bank and one that deduplicates in flight by payload key alike
// (`serialize = dedupe_by_bank or dedupe_in_flight_payload_key is not None`,
// engine/memory_engine.py:23014-23021) — and raises
// `OperationValidationError("Bank '<id>' not found", 404)`, which is why
// `DELETE /banks/<unknown>/memories/<id>/observations` answers
// `404 {"detail":"Bank '<id>' not found"}` on a bank that never existed: the
// clear succeeds as a no-op and the auto-consolidation submit it makes is what
// reports the missing bank.
//
// Restoring the old tolerate-and-let-the-foreign-key-fail behaviour makes both
// cases fail here with a constraint violation instead of ErrBankNotFound.
func TestEnqueueConsolidationUnknownBankIsNotFound(t *testing.T) {
	pg, _ := newClearMemoriesStore(t, "enqueue_nobank")
	ctx := context.Background()

	// Unscoped: the dedupe-by-bank path.
	if _, _, err := pg.EnqueueConsolidation(ctx, "no-such-bank"); !errors.Is(err, ErrBankNotFound) {
		t.Fatalf("unscoped enqueue on unknown bank = %v, want ErrBankNotFound", err)
	}
	// Scoped: the unconditional path, which inserts directly.
	if _, _, err := pg.EnqueueConsolidation(ctx, "no-such-bank", []string{"team:exec"}); !errors.Is(err, ErrBankNotFound) {
		t.Fatalf("scoped enqueue on unknown bank = %v, want ErrBankNotFound", err)
	}
	// The explicit /consolidate submit.
	if _, _, err := pg.EnqueueCallerRequestedConsolidation(ctx, "no-such-bank"); !errors.Is(err, ErrBankNotFound) {
		t.Fatalf("caller-requested enqueue on unknown bank = %v, want ErrBankNotFound", err)
	}

	// A bank that exists still enqueues, so the guard is not a blanket refusal.
	if _, err := pg.CreateBank(ctx, Bank{BankID: "real", Name: "real"}); err != nil {
		t.Fatal(err)
	}
	op, deduplicated, err := pg.EnqueueConsolidation(ctx, "real")
	if err != nil {
		t.Fatalf("enqueue on a real bank: %v", err)
	}
	if deduplicated || op.OperationID == "" {
		t.Fatalf("first enqueue = (%q, dedup=%v), want a fresh operation", op.OperationID, deduplicated)
	}
	if _, again, err := pg.EnqueueConsolidation(ctx, "real"); err != nil || !again {
		t.Fatalf("second enqueue = (%v, dedup=%v), want deduplicated", err, again)
	}
}
