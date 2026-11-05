package consolidation

import (
	"context"
	"math"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// embeddingStore is a stub store that carries the deployment's consolidation
// tuning and the embedding capability the duplicate probe needs, so a test can
// drive the cosine mechanism without a model or a database.
type embeddingStore struct {
	*stubStore
	tuning storage.ConsolidationTuning
	// embedded mirrors the real store's "can this deployment embed" answer:
	// false is a deployment with no embedding model.
	embedded bool
	probe    []float32
	// candidates are the in-scope observations the probe matched, each with its
	// stored embedding.
	candidates []Observation
}

func (s *embeddingStore) ConsolidationTuning() storage.ConsolidationTuning { return s.tuning }

func (s *embeddingStore) ObservationDuplicates(context.Context, string, []string, string, int) ([]float32, []Observation, bool, error) {
	return s.probe, s.candidates, s.embedded, nil
}

// embeddingStoreWithTuning builds a store over an in-scope twin the probe would
// return, with the reference's shipped tuning so the default threshold applies.
func embeddingStoreWithTuning(store *stubStore, candidates []Observation) *embeddingStore {
	return &embeddingStore{
		stubStore: store, tuning: storage.DefaultConsolidationTuning(),
		embedded: true, probe: []float32{1, 0}, candidates: candidates,
	}
}

// createDecision builds a model that answers with a single create for one fact.
func createDecision(text string) provider.CompletionResult {
	return provider.CompletionResult{Structured: map[string]any{
		"creates": []any{map[string]any{"text": text, "source_fact_ids": []any{"f1"}}},
	}}
}

// mergeVerdictResp is the focused "merge" answer carrying a synthesized text.
func mergeVerdictResp(text string) provider.CompletionResult {
	return provider.CompletionResult{Structured: map[string]any{
		"action": "merge", "text": text, "reason": "same fact",
	}}
}

// keepVerdictResp is the focused "keep" answer.
func keepVerdictResp() provider.CompletionResult {
	return provider.CompletionResult{Structured: map[string]any{
		"action": "keep", "text": "", "reason": "different detail",
	}}
}

// A cosine twin above the threshold is only folded when the focused verdict
// says "merge"; the synthesized text then overwrites the twin's original text
// and the skipped create's evidence is attached to it.
func TestConsolidatorMergesDuplicateOnMergeVerdict(t *testing.T) {
	const merged = "Alice owns a red Honda and drives the red Honda"
	store := embeddingStoreWithTuning(
		&stubStore{facts: []Fact{{ID: "f1", Text: "Alice owns a Honda"}}},
		// Cosine 1.0 to the probe [1, 0], well above the 0.97 default.
		[]Observation{{ID: "o1", Text: "Alice owns a red Honda", Embedding: []float32{2, 0}}},
	)
	model := &stubProvider{
		name: "openai",
		responses: []provider.CompletionResult{
			createDecision("Alice drives the red Honda"),
			mergeVerdictResp(merged),
		},
	}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want the merged create skipped", result)
	}
	if len(store.created) != 0 {
		t.Fatalf("a merged duplicate must not be written: %+v", store.created)
	}
	// The verdict's synthesized text overwrites the twin, and the create's
	// evidence is attached to it. The old path folded with the twin's own text,
	// so this assertion is what the adjudication changes.
	if len(store.updated) != 1 || store.updated[0].ObservationID != "o1" || store.updated[0].Text != merged {
		t.Fatalf("updated = %+v, want o1 rewritten with the synthesized text", store.updated)
	}
	if len(store.marked) != 1 || store.marked[0] != "f1" {
		t.Fatalf("marked = %v, want the folded fact", store.marked)
	}
}

// A create whose embedding is below the threshold is a genuinely different
// conclusion and is written, with no verdict asked at all.
func TestConsolidatorKeepsCreateBelowEmbeddingThreshold(t *testing.T) {
	store := embeddingStoreWithTuning(
		&stubStore{facts: []Fact{{ID: "f1", Text: "Alice owns a Honda"}}},
		// Cosine 0 to the probe [1, 0], below the 0.97 default.
		[]Observation{{ID: "o1", Text: "unrelated", Embedding: []float32{0, 1}}},
	)
	model := &stubProvider{name: "openai", response: createDecision("Alice works late")}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 0 {
		t.Fatalf("result = %+v, want the below-threshold create written", result)
	}
	if len(store.created) != 1 || store.created[0].Text != "Alice works late" {
		t.Fatalf("created = %+v", store.created)
	}
	if model.callCount() != 1 {
		t.Fatalf("completions = %d, want only the main consolidation call", model.callCount())
	}
}

// The threshold's own value decides: the same pair of embeddings is a duplicate
// when the threshold sits below their cosine and novel when it sits above. A
// duplicate is only folded when the focused verdict agrees to merge.
func TestConsolidatorDedupThresholdMovesTheDecision(t *testing.T) {
	// Cosine of [1, 0] and [1, 1] is 1/sqrt(2) ~= 0.7071.
	pair := []Observation{{ID: "o1", Text: "a twin", Embedding: []float32{1, 1}}}

	for _, tc := range []struct {
		name      string
		threshold float64
		duplicate bool
	}{
		{"threshold above the cosine keeps it novel", 0.8, false},
		{"threshold below the cosine makes it a duplicate", 0.7, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tuning := storage.DefaultConsolidationTuning()
			tuning.DedupThreshold = tc.threshold
			store := embeddingStoreWithTuning(
				&stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}}, pair,
			)
			store.tuning = tuning
			model := &stubProvider{name: "openai", response: createDecision("a new conclusion")}
			if tc.duplicate {
				model.responses = []provider.CompletionResult{
					createDecision("a new conclusion"),
					mergeVerdictResp("a merged twin"),
				}
			}

			result, err := New(model, store).Run(context.Background(), "b1")
			if err != nil {
				t.Fatal(err)
			}
			if tc.duplicate {
				if result.Skipped != 1 || result.Created != 0 {
					t.Fatalf("result = %+v, want the create skipped at threshold %v", result, tc.threshold)
				}
			} else if result.Created != 1 || result.Skipped != 0 {
				t.Fatalf("result = %+v, want the create written at threshold %v", result, tc.threshold)
			}
		})
	}
}

// A threshold of 1 disables the semantic dedup: an identical embedding still
// writes, because the operator asked for the feature off.
func TestConsolidatorDedupDisabledAtThresholdOne(t *testing.T) {
	tuning := storage.DefaultConsolidationTuning()
	tuning.DedupThreshold = 1
	store := embeddingStoreWithTuning(
		&stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}},
		[]Observation{{ID: "o1", Text: "identical twin", Embedding: []float32{1, 0}}},
	)
	store.tuning = tuning
	model := &stubProvider{name: "openai", response: createDecision("a new conclusion")}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 0 {
		t.Fatalf("result = %+v, want dedup disabled at threshold 1", result)
	}
}

// A deployment with no embedding model runs no duplicate detection at all: the
// token-similarity floor is gone, so a restated conclusion is kept as its own
// observation rather than folded on a different scale.
func TestConsolidatorNoEmbeddingSkipsDedup(t *testing.T) {
	store := &embeddingStore{
		stubStore: &stubStore{
			facts: []Fact{{ID: "f1", Text: "Alice owns a Honda"}},
			// The pool still holds a near-identical twin; without a probe it
			// must not be consulted.
			observations: []Observation{{ID: "o1", Text: "Alice owns a red Honda car"}},
		},
		tuning:   storage.DefaultConsolidationTuning(),
		embedded: false,
		probe:    []float32{1, 0},
	}
	model := &stubProvider{name: "openai", response: createDecision("Alice owns the red Honda car")}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Skipped != 0 {
		t.Fatalf("result = %+v, want the create kept without an embedding model", result)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %+v, want the restated conclusion written", store.created)
	}
	// No focused verdict was asked: without a probe there is nothing to decide.
	if model.callCount() != 1 {
		t.Fatalf("completions = %d, want only the main consolidation call", model.callCount())
	}
}

// cosineSimilarity scores a pair in [-1, 1], and an unusable pair — unequal
// widths or a zero vector — scores 0 rather than a NaN that could clear any
// threshold.
func TestCosineSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"equal direction different magnitude", []float32{1, 1}, []float32{5, 5}, 1},
		{"unequal widths", []float32{1, 0}, []float32{1}, 0},
		{"zero vector", []float32{1, 0}, []float32{0, 0}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cosineSimilarity(tc.a, tc.b)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("cosine(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
