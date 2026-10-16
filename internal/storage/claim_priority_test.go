package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

// putConsolidation records a pending consolidation operation for a bank at a
// given enqueue time. PutOperation stamps creation, so the time is set directly
// afterwards to make the tie-break deterministic.
func putConsolidation(t *testing.T, store *MemoryStore, id, bankID string, createdAt time.Time) {
	t.Helper()
	if _, err := store.PutOperation(context.Background(), Operation{
		OperationID:   id,
		BankID:        bankID,
		OperationType: OperationTypeConsolidate,
		Status:        StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	op := store.operations[id]
	op.CreatedAt = createdAt
	op.UpdatedAt = createdAt
	store.operations[id] = op
	store.mu.Unlock()
}

// test-consolidation-claim-priority: a consolidation claim prefers the bank
// whose identifier matches a higher-priority pattern, even when a lower-priority
// bank queued its work first.
func TestConsolidationClaimPrefersHigherPriorityBank(t *testing.T) {
	store := NewMemoryStore("public")
	base := time.Now().UTC()
	putConsolidation(t, store, "old", "low-priority", base)
	putConsolidation(t, store, "new", "critical", base.Add(time.Second))

	op, err := store.ClaimConsolidationByPriority(context.Background(), "w1", time.Hour,
		map[string]int{"critical": 10, "*": 1})
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationID != "new" {
		t.Fatalf("claimed %s, want the higher-priority bank's work", op.OperationID)
	}
}

// test-consolidation-claim-priority-tie: within one tier the oldest work is
// claimed first, matching the reference's per-tier ordering.
func TestConsolidationClaimTieBreaksOldestFirst(t *testing.T) {
	store := NewMemoryStore("public")
	base := time.Now().UTC()
	putConsolidation(t, store, "later", "bank-a", base.Add(time.Second))
	putConsolidation(t, store, "earlier", "bank-b", base)

	op, err := store.ClaimConsolidationByPriority(context.Background(), "w1", time.Hour,
		map[string]int{"bank-*": 5})
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationID != "earlier" {
		t.Fatalf("claimed %s, want the oldest of the tier", op.OperationID)
	}
}

// test-consolidation-claim-specific-outranks-catch-all: a bank matching a
// specific pattern uses that pattern's priority even when the catch-all is
// higher.
func TestConsolidationClaimSpecificPatternOutranksCatchAll(t *testing.T) {
	store := NewMemoryStore("public")
	base := time.Now().UTC()
	putConsolidation(t, store, "catch-all", "plain", base)
	putConsolidation(t, store, "specific", "shadow-1", base.Add(time.Second))

	// The catch-all is higher, but the specific pattern wins for its bank.
	op, err := store.ClaimConsolidationByPriority(context.Background(), "w1", time.Hour,
		map[string]int{"shadow-*": 2, "*": 10})
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationID != "catch-all" {
		t.Fatalf("claimed %s, want the bank on the higher catch-all tier", op.OperationID)
	}
}

// test-consolidation-claim-only-consolidation: the priority claim considers only
// consolidation work, so an older operation of another type is left for the
// shared claim.
func TestConsolidationClaimIgnoresOtherOperationTypes(t *testing.T) {
	store := NewMemoryStore("public")
	base := time.Now().UTC()
	if _, err := store.PutOperation(context.Background(), Operation{
		OperationID: "other", BankID: "bank-a", OperationType: OperationTypeTransferImport,
		Status: StatusPending, CreatedAt: base, UpdatedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	putConsolidation(t, store, "cons", "bank-a", base.Add(time.Second))

	op, err := store.ClaimConsolidationByPriority(context.Background(), "w1", time.Hour,
		map[string]int{"bank-a": 3})
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationID != "cons" {
		t.Fatalf("claimed %s, want the consolidation operation", op.OperationID)
	}
}

// test-consolidation-claim-no-priority: with no priority configured the claim
// falls back to the oldest consolidation, so an unset key changes nothing.
func TestConsolidationClaimWithoutPriorityClaimsOldest(t *testing.T) {
	store := NewMemoryStore("public")
	base := time.Now().UTC()
	putConsolidation(t, store, "old", "bank-a", base)
	putConsolidation(t, store, "new", "bank-b", base.Add(time.Second))

	op, err := store.ClaimConsolidationByPriority(context.Background(), "w1", time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if op.OperationID != "old" {
		t.Fatalf("claimed %s, want the oldest consolidation", op.OperationID)
	}
}

// likeMatch is the in-memory form of the SQL LIKE the claim translates a
// pattern to; anchored at both ends, "*" spans any run.
func TestLikeMatchAnchorsAndWildcards(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"*", "anything", true},
		{"bank-a", "bank-a", true},
		{"bank-a", "bank-a-2", false},
		{"bank-*", "bank-a", true},
		{"bank-*", "other", false},
		{"*-prod", "billing-prod", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxcyyb", false},
	}
	for _, tc := range cases {
		if got := likeMatch(tc.pattern, tc.value); got != tc.want {
			t.Errorf("likeMatch(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}

// bankPriority applies the specific-then-catch-all precedence the reference
// documents, falling back to tier one when nothing is configured.
func TestBankPriorityPrecedence(t *testing.T) {
	cases := []struct {
		name     string
		priority map[string]int
		bank     string
		want     int
	}{
		{"specific wins over a higher catch-all", map[string]int{"shadow-*": 2, "*": 10}, "shadow-1", 2},
		{"catch-all applies to an unlisted bank", map[string]int{"shadow-*": 2, "*": 10}, "plain", 10},
		{"highest matching specific wins", map[string]int{"b*": 2, "billing": 7}, "billing", 7},
		{"implicit tier one without a catch-all", map[string]int{"shadow-*": 2}, "plain", 1},
	}
	for _, tc := range cases {
		if got := bankPriority(tc.priority, tc.bank); got != tc.want {
			t.Errorf("%s: bankPriority(%v, %q) = %d, want %d", tc.name, tc.priority, tc.bank, got, tc.want)
		}
	}
}

// consolidationPriorityRank renders a real ranking expression: a specific
// pattern's tier, with the catch-all as the fallback for an unlisted bank.
func TestConsolidationPriorityRankExpression(t *testing.T) {
	specific := consolidationPriorityRank(map[string]int{"shadow-*": 2, "*": 10})
	if !strings.Contains(specific, "('shadow-%'::text, 2)") {
		t.Fatalf("rank expression does not convert the pattern: %s", specific)
	}
	if !strings.HasSuffix(specific, ", 10)") {
		t.Fatalf("rank expression does not carry the catch-all fallback: %s", specific)
	}

	// With only a catch-all, the rank is the literal tier; no subquery is built.
	if got := consolidationPriorityRank(map[string]int{"*": 4}); got != "4" {
		t.Fatalf("catch-all-only rank = %q, want 4", got)
	}
	// With no catch-all the fallback is the reference's implicit tier of one.
	if got := consolidationPriorityRank(map[string]int{"billing": 9}); !strings.HasSuffix(got, ", 1)") {
		t.Fatalf("rank without a catch-all = %q, want a fallback of 1", got)
	}
}
