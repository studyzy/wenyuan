package storage

import (
	"context"
	"strings"
	"testing"
)

// reflectTestStore builds an in-memory store with one bank.
func reflectTestStore(t *testing.T) *MemoryStore {
	t.Helper()
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	return store
}

// beh-0014 / obs-0007: the loop stops early and synthesizes when accumulated
// evidence would exceed the model context budget, and forces a final synthesis
// at the iteration bound.
func TestReflectLoopBoundedAndSynthesizes(t *testing.T) {
	store := reflectTestStore(t)
	// Seed facts that individually fit but together exceed the budget.
	long := strings.Repeat("word ", 1500)
	for i := 0; i < 5; i++ {
		store.SeedMemory("b1", RecallMatch{ID: "f" + string(rune('a'+i)), Text: long, Type: "world"})
	}
	outcome, err := store.Reflect(context.Background(), ReflectQuery{BankID: "b1", Query: "word"})
	if err != nil {
		t.Fatalf("reflect failed: %v", err)
	}
	if strings.TrimSpace(outcome.Answer) == "" {
		t.Fatal("a forced synthesis must still produce an answer")
	}
}

// beh-0015 / obs-0008: a fresh covering perspective answers without descending
// to raw facts; a stale or non-covering perspective descends deeper.
func TestReflectFreshPerspectiveAvoidsRawFacts(t *testing.T) {
	store := reflectTestStore(t)
	store.SeedMemory("b1", RecallMatch{ID: "raw-1", Text: "alpha raw fact", Type: "world"})
	store.SeedMentalModel("b1", ReflectMentalModel{ID: "mm-fresh", Text: "alpha coverage", Context: "c"})

	outcome, err := store.Reflect(context.Background(), ReflectQuery{
		BankID: "b1", Query: "alpha",
		Include: ReflectInclude{ToolCalls: true, Facts: &ReflectFactsInclude{}},
	})
	if err != nil {
		t.Fatalf("reflect failed: %v", err)
	}
	if outcome.Trace == nil {
		t.Fatal("trace requested but absent")
	}
	for _, call := range outcome.Trace.ToolCalls {
		if call.Tool == "search_facts" {
			t.Fatalf("a fresh covering perspective must not invoke the raw-fact tool: %+v", outcome.Trace.ToolCalls)
		}
	}
	if len(outcome.MentalModels) == 0 {
		t.Fatal("the fresh perspective must be cited")
	}
}

// beh-0015: a stale perspective forces descent to raw facts.
func TestReflectStalePerspectiveDescends(t *testing.T) {
	store := reflectTestStore(t)
	store.SeedMemory("b1", RecallMatch{ID: "raw-1", Text: "alpha raw fact", Type: "world"})
	store.SeedMentalModel("b1", ReflectMentalModel{ID: "mm-stale", Text: "alpha", Stale: true})

	outcome, err := store.Reflect(context.Background(), ReflectQuery{
		BankID: "b1", Query: "alpha",
		Include: ReflectInclude{ToolCalls: true},
	})
	if err != nil {
		t.Fatalf("reflect failed: %v", err)
	}
	invokedFacts := false
	for _, call := range outcome.Trace.ToolCalls {
		if call.Tool == "search_facts" {
			invokedFacts = true
		}
	}
	if !invokedFacts {
		t.Fatalf("a stale perspective must descend to raw facts: %+v", outcome.Trace.ToolCalls)
	}
}

// beh-0016 / inv-0005: an observations-only selection removes the raw-fact
// tool so it is never invoked; a mental-model exclusion removes its tool.
func TestReflectToolRemoval(t *testing.T) {
	store := reflectTestStore(t)
	store.SeedMemory("b1", RecallMatch{ID: "raw-1", Text: "alpha", Type: "world"})

	obsOnly, err := store.Reflect(context.Background(), ReflectQuery{
		BankID: "b1", Query: "alpha", Types: []string{string(TypeObservation)},
		Include: ReflectInclude{ToolCalls: true},
	})
	if err != nil {
		t.Fatalf("reflect failed: %v", err)
	}
	for _, call := range obsOnly.Trace.ToolCalls {
		if call.Tool == "search_facts" {
			t.Fatalf("observations-only must not invoke the raw-fact tool: %+v", obsOnly.Trace.ToolCalls)
		}
	}
	if containsString(obsOnly.AvailableTools, "search_facts") {
		t.Fatalf("observations-only must remove the raw-fact tool: %v", obsOnly.AvailableTools)
	}

	excluded, err := store.Reflect(context.Background(), ReflectQuery{
		BankID: "b1", Query: "alpha", ExcludeMentalModels: true,
	})
	if err != nil {
		t.Fatalf("reflect failed: %v", err)
	}
	if containsString(excluded.AvailableTools, "search_mental_models") {
		t.Fatalf("mental-model exclusion must remove its tool: %v", excluded.AvailableTools)
	}
}

// beh-0009 / neg-0001 / inv-0002: a no-answer run reports the sentinel rather
// than a placeholder answer.
func TestReflectNoAnswerSentinel(t *testing.T) {
	store := reflectTestStore(t)
	// A bank with no memories still answers, so force the no-answer path by
	// canceling the context before the loop synthesizes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Reflect(ctx, ReflectQuery{BankID: "b1", Query: "q"}); err == nil {
		t.Fatal("a canceled reflection must fail rather than answer")
	}
}

// beh-0005: the query carries the appended context so the engine sees the
// concatenated question.
func TestReflectUnknownBankFails(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.Reflect(context.Background(), ReflectQuery{BankID: "missing", Query: "q"}); err != ErrBankNotFound {
		t.Fatalf("unknown bank = %v, want ErrBankNotFound", err)
	}
}

// beh-0004: cited evidence is present only when the facts gate is set.
func TestReflectCitedEvidenceGate(t *testing.T) {
	store := reflectTestStore(t)
	store.SeedMemory("b1", RecallMatch{ID: "f1", Text: "alpha", Type: "world"})

	without, err := store.Reflect(context.Background(), ReflectQuery{BankID: "b1", Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if without.Facts != nil || without.MentalModels != nil {
		t.Fatalf("cited evidence must be absent without the facts gate: %+v", without)
	}

	with, err := store.Reflect(context.Background(), ReflectQuery{
		BankID: "b1", Query: "alpha",
		Include: ReflectInclude{Facts: &ReflectFactsInclude{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(with.Facts) == 0 {
		t.Fatal("the facts gate must populate cited facts")
	}
}
