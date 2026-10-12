package storage

import (
	"context"
	"strings"
	"testing"
)

// beh-0019: a provider-prefixed or suffixed tool name normalizes to the same
// tool; an unrecognized name is not treated as known.
func TestNormalizeToolName(t *testing.T) {
	cases := map[string]string{
		"search_facts":           "search_facts",
		"tool_search_facts":      "search_facts",
		"tools/search_facts":     "search_facts",
		"functions.search_facts": "search_facts",
		"function_search_facts":  "search_facts",
		"call_search_facts":      "search_facts",
		"search_facts_tool":      "search_facts",
		"search_facts_function":  "search_facts",
		"search_facts_call":      "search_facts",
		"  search_facts  ":       "search_facts",
	}
	for in, want := range cases {
		if got := normalizeToolName(in); got != want {
			t.Fatalf("normalizeToolName(%q) = %q, want %q", in, got, want)
		}
	}
	if !isKnownTool("tool_search_facts") {
		t.Fatal("a normalized known tool must be recognized")
	}
	if isKnownTool("tool_totally_unknown") {
		t.Fatal("an unrecognized tool must not be treated as known")
	}
}

// beh-0020: duplicate call identifiers within one turn are made unique.
func TestUniqueCallID(t *testing.T) {
	seen := map[string]int{}
	first := uniqueCallID("call", seen)
	second := uniqueCallID("call", seen)
	third := uniqueCallID("call", seen)
	if first == second || second == third || first == third {
		t.Fatalf("duplicate call identifiers must be made unique: %q %q %q", first, second, third)
	}
	if first != "call" {
		t.Fatalf("the first occurrence must keep the original id: %q", first)
	}
}

// beh-0018 / obs-0010: an over-length answer is shortened to the visible-length
// budget, and a zero budget leaves it whole.
func TestApplyLengthRewrite(t *testing.T) {
	long := strings.Repeat("word ", 50)
	shortened := applyLengthRewrite(long, 10)
	if reflectTokenCount(shortened) > 10 {
		t.Fatalf("rewrite must bound the answer: %q", shortened)
	}
	if applyLengthRewrite(long, 0) != long {
		t.Fatal("a zero budget must leave the answer whole")
	}
	short := "two words"
	if applyLengthRewrite(short, 100) != short {
		t.Fatal("an answer within budget must be unchanged")
	}
}

// beh-0020 / tmg-0004: recorded tool calls follow the model's call order and
// each call keeps its own result.
func TestToolCallsOrderedAndDistinct(t *testing.T) {
	store := reflectTestStore(t)
	store.SeedMemory("b1", RecallMatch{ID: "f1", Text: "alpha", Type: "world"})
	outcome, err := store.Reflect(context.Background(), ReflectQuery{
		BankID: "b1", Query: "alpha",
		Include: ReflectInclude{ToolCalls: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Trace.ToolCalls) < 2 {
		t.Fatalf("expected multiple tool calls: %+v", outcome.Trace.ToolCalls)
	}
	lastIteration := 0
	for _, call := range outcome.Trace.ToolCalls {
		if call.Iteration < lastIteration {
			t.Fatalf("tool calls must follow iteration order: %+v", outcome.Trace.ToolCalls)
		}
		lastIteration = call.Iteration
	}
	seen := map[string]bool{}
	for _, call := range outcome.Trace.ToolCalls {
		if call.ID == "" {
			t.Fatalf("every tool call must carry a wire identifier: %+v", call)
		}
		if seen[call.ID] {
			t.Fatalf("tool call identifiers must be unique: %s", call.ID)
		}
		seen[call.ID] = true
	}
}

// beh-0021 / beh-0022: a canceled context is propagated without being retried
// or wrapped.
func TestReflectCancellationPropagates(t *testing.T) {
	store := reflectTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.Reflect(ctx, ReflectQuery{BankID: "b1", Query: "q"})
	if err != context.Canceled {
		t.Fatalf("cancellation = %v, want context.Canceled", err)
	}
}

// beh-0014 / tmg-0002: the loop stops early once evidence would exceed the
// context budget rather than exhausting the iteration bound.
func TestLoopStopsEarlyOnBudget(t *testing.T) {
	store := reflectTestStore(t)
	long := strings.Repeat("word ", 1500)
	for i := 0; i < 5; i++ {
		store.SeedMemory("b1", RecallMatch{ID: "f" + itoaSmall(i), Text: long, Type: "world"})
	}
	outcome, err := store.Reflect(context.Background(), ReflectQuery{
		BankID: "b1", Query: "word",
		Include: ReflectInclude{ToolCalls: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	maxIteration := 0
	for _, call := range outcome.Trace.ToolCalls {
		if call.Iteration > maxIteration {
			maxIteration = call.Iteration
		}
	}
	if maxIteration >= reflectMaxIterations {
		t.Fatalf("the loop must stop early once evidence crosses the budget: %+v", outcome.Trace.ToolCalls)
	}
}
