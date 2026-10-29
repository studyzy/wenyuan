package consolidation

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// The whole batch is stamped consolidated once the pass has answered it, and a
// fact the response left unaddressed in a multi-fact batch is re-asked — first
// as one group, then singly — before it is stamped too. A fact skipped while
// alone is the model's answer and is never re-asked.
func TestConsolidatorStampsBatchAndReasksUnaddressedFacts(t *testing.T) {
	store := &stubStore{
		facts: []Fact{
			{ID: "f1", Text: "Alice owns a Honda"},
			{ID: "f2", Text: "Alice likes tea"},
			{ID: "f3", Text: "Alice works late"},
		},
	}
	// The first response cites only f1, the re-asked group cites nothing, and
	// the final single-fact calls skip as well — chatter.
	model := &scriptedProvider{stubProvider: stubProvider{name: "openai"}, responses: []provider.CompletionResult{
		{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "Alice owns a Honda", "source_fact_ids": []any{"f1"}}},
		}},
		{Structured: map[string]any{}},
		{Structured: map[string]any{}},
		{Structured: map[string]any{}},
	}}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if model.calls != 4 {
		t.Fatalf("model calls = %d, want the batch, the group re-ask and two single-fact calls", model.calls)
	}
	if result.Created != 1 {
		t.Fatalf("created = %d, want the first response's create", result.Created)
	}
	// Every fact was answered — cited, re-asked to its cap, or skipped alone —
	// so every fact is stamped and the backlog stops answering.
	if uniqueMarked(store.marked) != 3 {
		t.Fatalf("marked = %v, want the whole batch stamped", store.marked)
	}
}

// A single-fact batch the model skips is stamped immediately: alone, the skip
// is the model's answer (chatter), never an oversight to re-ask.
func TestConsolidatorStampsSingleFactBatchImmediately(t *testing.T) {
	store := &stubStore{facts: []Fact{{ID: "f1", Text: "Alice said thanks"}}}
	model := &scriptedProvider{stubProvider: stubProvider{name: "openai"}, responses: []provider.CompletionResult{
		{Structured: map[string]any{}},
	}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want one call", model.calls)
	}
	if uniqueMarked(store.marked) != 1 {
		t.Fatalf("marked = %v, want the fact stamped", store.marked)
	}
}

// A re-asked fact gets at most two more looks before it is stamped anyway: a
// model that keeps skipping a fact must not pin it in the backlog forever.
func TestConsolidatorReaskCapsAtTwo(t *testing.T) {
	store := &stubStore{
		facts: []Fact{{ID: "f1", Text: "one"}, {ID: "f2", Text: "two"}},
	}
	model := &scriptedProvider{stubProvider: stubProvider{name: "openai"}, responses: []provider.CompletionResult{
		{Structured: map[string]any{}},
		{Structured: map[string]any{}},
		{Structured: map[string]any{}},
		{Structured: map[string]any{}},
	}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	// The batch, the group re-ask, then one single-fact call per fact — each
	// fact asked three times in all, never a fourth.
	if model.calls != 4 {
		t.Fatalf("model calls = %d, want batch + group + two singletons", model.calls)
	}
	if uniqueMarked(store.marked) != 2 {
		t.Fatalf("marked = %v, want both facts stamped at the cap", store.marked)
	}
}

// A model that answers the group re-ask stops the chain early: only the facts
// still unaddressed are re-asked again.
func TestConsolidatorReaskStopsWhenGroupAnswers(t *testing.T) {
	store := &stubStore{
		facts: []Fact{{ID: "f1", Text: "one"}, {ID: "f2", Text: "two"}},
	}
	model := &scriptedProvider{stubProvider: stubProvider{name: "openai"}, responses: []provider.CompletionResult{
		{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "one", "source_fact_ids": []any{"f1"}}},
		}},
		{Structured: map[string]any{
			"creates": []any{map[string]any{"text": "two", "source_fact_ids": []any{"f2"}}},
		}},
	}}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("model calls = %d, want the batch and the group re-ask", model.calls)
	}
	if uniqueMarked(store.marked) != 2 {
		t.Fatalf("marked = %v, want both facts stamped", store.marked)
	}
}
