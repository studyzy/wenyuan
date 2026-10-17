package storage

import (
	"context"
	"testing"
)

// capturingReflectRunner records the request it was handed and returns a canned
// outcome, so the dispatch can be asserted without a provider or a database.
type capturingReflectRunner struct {
	calls   int
	req     AgenticReflectRequest
	outcome ReflectOutcome
}

func (r *capturingReflectRunner) Run(_ context.Context, req AgenticReflectRequest) (ReflectOutcome, error) {
	r.calls++
	r.req = req
	return r.outcome, nil
}

// The store must hand a supplied runner the caller's bank, question, budget and
// bounds, expose itself as the retrieval surface, forward the candidate
// directives with their names, and surface the runner's answer. The runner is
// invoked before any database access, so this runs with no database at all.
func TestPostgresReflectDispatchesToSuppliedRunner(t *testing.T) {
	store := &PostgresStore{}
	runner := &capturingReflectRunner{outcome: ReflectOutcome{Answer: "the model's answer"}}

	outcome, err := store.Reflect(context.Background(), ReflectQuery{
		BankID:        "bank-1",
		Query:         "what changed?",
		Budget:        ReflectBudgetHigh,
		MaxTokens:     128,
		MaxIterations: 3,
		Reflection:    runner,
		Directives: []ReflectDirective{
			{ID: "d1", Name: "Terse", Content: "Be terse."},
			{ID: "d2", Name: "Empty", Content: ""},
		},
	})
	if err != nil {
		t.Fatalf("reflect dispatch failed: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner invoked %d times, want 1", runner.calls)
	}
	if runner.req.BankID != "bank-1" {
		t.Errorf("bank = %q, want %q", runner.req.BankID, "bank-1")
	}
	if runner.req.Question != "what changed?" {
		t.Errorf("question = %q, want %q", runner.req.Question, "what changed?")
	}
	if runner.req.Budget != ReflectBudgetHigh {
		t.Errorf("budget = %q, want %q", runner.req.Budget, ReflectBudgetHigh)
	}
	if runner.req.MaxTokens != 128 || runner.req.MaxIterations != 3 {
		t.Errorf("bounds = (%d, %d), want (128, 3)", runner.req.MaxTokens, runner.req.MaxIterations)
	}
	if runner.req.Retrieval != ReflectionRetrieval(store) {
		t.Errorf("retrieval = %T, want the store itself", runner.req.Retrieval)
	}
	if len(runner.req.Directives) != 2 ||
		runner.req.Directives[0].Name != "Terse" || runner.req.Directives[0].Content != "Be terse." ||
		runner.req.Directives[1].Name != "Empty" || runner.req.Directives[1].Content != "" {
		t.Errorf("directives = %+v, want the candidate directives with their names", runner.req.Directives)
	}
	if outcome.Answer != "the model's answer" {
		t.Errorf("answer = %q, want the runner's answer", outcome.Answer)
	}
}
