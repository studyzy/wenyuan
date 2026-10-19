package storage

import (
	"context"
	"testing"
	"time"
)

// groundingModel builds a model citing the given facts.
func groundingModel(cited ...map[string]any) MentalModel {
	items := make([]any, 0, len(cited))
	for _, fact := range cited {
		items = append(items, fact)
	}
	return MentalModel{
		ID: "mm-1", BankID: "b1", SourceQuery: "q",
		ReflectResult: map[string]any{"based_on": map[string]any{
			"memories":      items,
			"mental_models": []any{map[string]any{"id": "other-model", "type": "mental_model"}},
		}},
	}
}

// Only memory-backed citations count: a cited sibling document or directive
// addresses another table and must never be reported as missing.
func TestCitedGroundingFiltersByFactType(t *testing.T) {
	model := groundingModel(
		map[string]any{"id": "f1", "type": "world", "text": "Alice is on-call"},
		map[string]any{"id": "o1", "type": "observation", "text": "team is stable"},
		map[string]any{"id": "x1", "type": "mental_model", "text": "sibling"},
		map[string]any{"id": "d1", "type": "directive", "text": "rule"},
	)
	grounding := CitedGrounding(model)
	if len(grounding) != 2 {
		t.Fatalf("grounding = %+v, want only the memory-backed facts", grounding)
	}
	if grounding[0].ID != "f1" || grounding[1].ID != "o1" {
		t.Fatalf("grounding = %+v", grounding)
	}
}

// A missing citation is a retraction; a document whose every citation is gone
// is a broken link, not a retraction.
func TestRetractionOfGrounding(t *testing.T) {
	model := groundingModel(
		map[string]any{"id": "f1", "type": "world", "text": "Alice is on-call"},
		map[string]any{"id": "f2", "type": "experience", "text": "we shipped"},
	)
	partial := RetractionOf(model, map[string]bool{"f1": true})
	if len(partial.Facts) != 1 || partial.Facts[0].ID != "f2" || partial.CitedCount != 2 {
		t.Fatalf("retraction = %+v", partial)
	}
	if partial.Unresolvable() {
		t.Fatal("a partial retraction is not unresolvable")
	}
	// The recorded text survives with the entry, because the row it came from
	// may already be gone.
	if partial.Facts[0].Text != "we shipped" {
		t.Fatalf("the recorded text must be kept: %+v", partial.Facts[0])
	}

	none := RetractionOf(model, map[string]bool{})
	if !none.Unresolvable() {
		t.Fatal("losing every citation is unresolvable")
	}
	all := RetractionOf(model, map[string]bool{"f1": true, "f2": true})
	if len(all.Facts) != 0 {
		t.Fatalf("nothing is retracted when everything is live: %+v", all)
	}
}

// A model citing evidence that has gone is stale even though no watermark
// moved, and becomes fresh again once nothing it cites is missing.
func TestIsStaleDetectsRetractedGrounding(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	// One citation still resolves, so this is a retraction rather than a broken
	// link: a document whose every citation is gone is left alone.
	if _, err := store.UpsertUnit(ctx, MemoryUnit{ID: "live", BankID: "b1", Text: "still here"}); err != nil {
		t.Fatal(err)
	}
	seen := store.BankWriteWatermark(ctx, "b1")
	model := groundingModel(
		map[string]any{"id": "gone", "type": "world", "text": "a fact"},
		map[string]any{"id": "live", "type": "world", "text": "still here"},
	)
	model.LastMemorySeenAt = &seen
	if !store.IsStale(ctx, "b1", model) {
		t.Fatal("a model citing a missing fact must be stale")
	}

	// Write the missing fact and re-stamp, so the only thing that changed is
	// the evidence's presence.
	if _, err := store.UpsertUnit(ctx, MemoryUnit{ID: "gone", BankID: "b1", Text: "a fact"}); err != nil {
		t.Fatal(err)
	}
	// The watermark is stamped past the write, so the only thing that can make
	// the model stale now is the retraction check.
	ahead := time.Now().UTC().Add(time.Hour)
	model.LastMemorySeenAt = &ahead
	if store.IsStale(ctx, "b1", model) {
		t.Fatal("a model whose evidence all exists is not stale for that reason")
	}
}

// A model that cites nothing memory-backed is never stale for a retraction.
func TestIsStaleIgnoresModelsWithoutGrounding(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	seen := time.Now().UTC().Add(-time.Hour)
	model := MentalModel{
		ID: "mm-1", BankID: "b1",
		ReflectResult:    map[string]any{"based_on": map[string]any{"memories": []any{map[string]any{"id": "sibling", "type": "mental_model"}}}},
		LastMemorySeenAt: &seen,
	}
	if store.IsStale(ctx, "b1", model) {
		t.Fatal("a non-memory citation must not be read as a retraction")
	}
}

// A model whose every citation is gone is stale too: the question staleness
// asks is whether the document's grounding is intact, and none of it is. (The
// refresh draws a finer line when deciding what to do with the prose, but that
// does not make the document current.)
func TestIsStaleDetectsFullyRetractedGrounding(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	// A stamped watermark newer than the (unwritten) bank keeps the scoped half
	// of the check out of the way, so what is exercised here is the retraction.
	seen := time.Now().UTC()
	model := groundingModel(
		map[string]any{"id": "gone-1", "type": "world", "text": "a fact"},
		map[string]any{"id": "gone-2", "type": "experience", "text": "another"},
	)
	model.LastMemorySeenAt = &seen
	if !store.IsStale(ctx, "b1", model) {
		t.Fatal("a model citing nothing that still exists must be stale")
	}
}

// A listing asks the batch staleness question, which leaves the retraction
// check to the single-model path: a model whose evidence has gone still reads
// up to date there, while the detail read reports it stale.
func TestIsStaleBatchSkipsTheRetractionCheck(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, MemoryUnit{ID: "live", BankID: "b1", Text: "still here"}); err != nil {
		t.Fatal(err)
	}
	// The bank's write watermark is unset (UpsertUnit does not raise it) and the
	// model's stamp is newer, so the scoped half of the check is quiet for both
	// variants and the retraction is the only thing left to answer either.
	seen := time.Now().UTC()
	model := groundingModel(
		map[string]any{"id": "gone", "type": "world", "text": "a fact"},
		map[string]any{"id": "live", "type": "world", "text": "still here"},
	)
	model.LastMemorySeenAt = &seen
	if store.IsStaleBatch(ctx, "b1", model) {
		t.Fatal("the batch question must not ask about retracted grounding")
	}
	if !store.IsStale(ctx, "b1", model) {
		t.Fatal("the single-model question must still see the retraction")
	}
}
