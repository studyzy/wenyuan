package consolidation

import (
	"context"
	"errors"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// chainFacts builds n same-scope facts for a chain test.
func chainFacts(n int) []Fact {
	facts := make([]Fact, 0, n)
	for i := 0; i < n; i++ {
		facts = append(facts, Fact{ID: string(rune('a'+i/26)) + string(rune('a'+i%26)), Text: "fact"})
	}
	return facts
}

// chainConfig spreads the model call over the whole fetch batch, so a chain
// test's pass runs one batch per fetch.
func chainConfig() map[string]any {
	return map[string]any{"consolidation_llm_batch_size": 150}
}

// One pass folds up to consolidation_max_memories_per_round — 100 facts in
// fetch batches of 50 — and requeues a successor operation for the rest, so a
// round-capped chain drains a backlog that outlasts one operation.
func TestConsolidatorRoundCapRequeuesSuccessor(t *testing.T) {
	store := &capableStore{stubStore: &stubStore{
		facts:  chainFacts(150),
		config: chainConfig(),
	}}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The reads arrived in batches of 50, and the round folded 100.
	if store.factsLimit != 50 {
		t.Fatalf("load batch = %d, want the reference's 50", store.factsLimit)
	}
	if uniqueMarked(store.marked) != 100 {
		t.Fatalf("marked = %d, want the round cap of 100 folded", uniqueMarked(store.marked))
	}
	if result.Failed != 0 {
		t.Fatalf("failed = %d, want none", result.Failed)
	}
	if len(store.successors) != 1 || store.successors[0] {
		t.Fatalf("successors = %v, want one automatic requeue", store.successors)
	}
}

// A bank that drains below the round cap never requeues: the pass finished the
// work, and a successor would only wake to find nothing.
func TestConsolidatorDrainedBankDoesNotRequeue(t *testing.T) {
	store := &capableStore{stubStore: &stubStore{
		facts:  chainFacts(80),
		config: chainConfig(),
	}}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if uniqueMarked(store.marked) != 80 {
		t.Fatalf("marked = %d, want the whole backlog folded", uniqueMarked(store.marked))
	}
	if len(store.successors) != 0 {
		t.Fatalf("successors = %v, want none", store.successors)
	}
}

// An automatic chain stops re-queuing once auto-consolidation is off: the
// round folds, and the facts past the cap wait for the next trigger.
func TestConsolidatorAutoChainStopsWhenAutoConsolidationOff(t *testing.T) {
	config := chainConfig()
	config["enable_auto_consolidation"] = false
	store := &capableStore{stubStore: &stubStore{
		facts:  chainFacts(150),
		config: config,
	}}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if uniqueMarked(store.marked) != 100 {
		t.Fatalf("marked = %d, want the round folded either way", uniqueMarked(store.marked))
	}
	if len(store.successors) != 0 {
		t.Fatalf("successors = %v, want the automatic chain stopped", store.successors)
	}
}

// A chain an explicit /consolidate call started keeps draining the backlog even
// when auto-consolidation is off, and the successor carries caller_requested.
func TestConsolidatorCallerChainKeepsDraining(t *testing.T) {
	config := chainConfig()
	config["enable_auto_consolidation"] = false
	store := &capableStore{stubStore: &stubStore{
		facts:  chainFacts(150),
		config: config,
	}}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).WithCallerRequested(true).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.successors) != 1 || !store.successors[0] {
		t.Fatalf("successors = %v, want one caller-requested requeue", store.successors)
	}
}

// failingSuccessorStore records that its successor enqueue raises, so a test
// can assert the chain failure propagates instead of being swallowed.
type failingSuccessorStore struct {
	*capableStore
}

func (s *failingSuccessorStore) EnqueueConsolidationSuccessor(context.Context, string, bool) error {
	return errors.New("queue down")
}

// A successor enqueue that fails fails the pass: swallowing it would leave the
// bank with backlog and no queued work — silently stuck.
func TestConsolidatorSuccessorFailurePropagates(t *testing.T) {
	inner := &capableStore{stubStore: &stubStore{
		facts:  chainFacts(150),
		config: chainConfig(),
	}}
	store := &failingSuccessorStore{capableStore: inner}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Structured: map[string]any{}}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err == nil {
		t.Fatal("run: want the requeue failure to propagate")
	}
}
