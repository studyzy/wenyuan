package storage

import (
	"context"
	"fmt"
	"testing"
)

// The budget level maps to the candidate bound it allows, with an absent or
// unrecognised level falling back to the middle.
func TestBudgetLimit(t *testing.T) {
	tests := []struct {
		name   string
		budget string
		want   int
	}{
		{name: "low", budget: BudgetLow, want: BudgetLowLimit},
		{name: "mid", budget: BudgetMid, want: BudgetMidLimit},
		{name: "high", budget: BudgetHigh, want: BudgetHighLimit},
		{name: "absent falls back to mid", budget: "", want: BudgetMidLimit},
		{name: "unrecognised falls back to mid", budget: "enormous", want: BudgetMidLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DefaultTuning().BudgetLimit(test.budget); got != test.want {
				t.Fatalf("BudgetLimit(%q) = %d, want %d", test.budget, got, test.want)
			}
		})
	}
}

// The levels are ordered, so a higher budget never allows fewer candidates.
func TestBudgetLevelsAreOrdered(t *testing.T) {
	if BudgetLowLimit >= BudgetMidLimit || BudgetMidLimit >= BudgetHighLimit {
		t.Fatalf("budget limits are not increasing: %d, %d, %d", BudgetLowLimit, BudgetMidLimit, BudgetHighLimit)
	}
}

// A low-budget recall returns no more results than its bound, which is what
// makes the level a bound on work rather than a label.
func TestRecallHonoursBudgetBound(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	// Write more facts than the low bound so the cap has to bite, but fewer
	// than the middle bound, so the levels are told apart by the cap rather
	// than by how much was written.
	const written = BudgetLowLimit + 20
	items := make([]RetainItem, 0, written)
	for i := 0; i < written; i++ {
		items = append(items, RetainItem{
			Content:    fmt.Sprintf("fact number %d about the topic", i),
			DocumentID: fmt.Sprintf("doc-%d", i),
		})
	}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{BankID: "b1", Items: items}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		budget string
		want   int
	}{
		{name: "low is capped at its bound", budget: BudgetLow, want: BudgetLowLimit},
		{name: "mid is above the low bound", budget: BudgetMid, want: written},
		{name: "high is above the low bound", budget: BudgetHigh, want: written},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := store.Recall(context.TODO(), RecallQuery{BankID: "b1", Query: "fact topic", Budget: test.budget})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Matches) != test.want {
				t.Fatalf("budget %q returned %d matches, want %d", test.budget, len(result.Matches), test.want)
			}
		})
	}
}

// An explicit limit overrides the budget, so a caller that names one gets what
// it asked for.
func TestRecallExplicitLimitOverridesBudget(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	items := make([]RetainItem, 0, 10)
	for i := 0; i < 10; i++ {
		items = append(items, RetainItem{Content: fmt.Sprintf("fact %d", i), DocumentID: fmt.Sprintf("doc-%d", i)})
	}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{BankID: "b1", Items: items}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Recall(context.TODO(), RecallQuery{BankID: "b1", Query: "fact", Budget: BudgetHigh, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 3 {
		t.Fatalf("explicit limit returned %d matches, want 3", len(result.Matches))
	}
}
