package consolidation

import (
	"context"
	"math"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// embeddingStore is a stub store that carries the deployment's consolidation
// tuning and the embedding capability the reference's dedup probe needs, so a
// test can drive the cosine mechanism without a model or a database.
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

// A create whose embedding is at or above the configured threshold is a
// duplicate of the twin it matched: it is not written, and its evidence is
// folded into the twin instead of forming a second conclusion.
func TestConsolidatorSkipsCreateAboveEmbeddingThreshold(t *testing.T) {
	store := embeddingStoreWithTuning(
		&stubStore{facts: []Fact{{ID: "f1", Text: "Alice owns a Honda"}}},
		// Cosine 1.0 to the probe [1, 0], well above the 0.97 default.
		[]Observation{{ID: "o1", Text: "Alice owns a red Honda", Embedding: []float32{2, 0}}},
	)
	model := &stubProvider{name: "openai", response: createDecision("Alice drives the red Honda")}

	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want the above-threshold create skipped", result)
	}
	if len(store.created) != 0 {
		t.Fatalf("a duplicate must not be written: %+v", store.created)
	}
	// The duplicate's evidence is attached to the twin, not dropped.
	if len(store.updated) != 1 || store.updated[0].ObservationID != "o1" {
		t.Fatalf("updated = %+v, want the create folded into o1", store.updated)
	}
	if len(store.marked) != 1 || store.marked[0] != "f1" {
		t.Fatalf("marked = %v, want the folded fact", store.marked)
	}
}

// A create whose embedding is below the threshold is a genuinely different
// conclusion and is written.
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
}

// The threshold's own value decides: the same pair of embeddings is a duplicate
// when the threshold sits below their cosine and novel when it sits above.
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

// A threshold of 1 disables the semantic dedup, matching the reference's
// _dedup_active gate: an identical embedding still writes, because the operator
// asked for the feature off.
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

// Without an embedding model the pass keeps its token-similarity floor, so a
// restated conclusion is still caught rather than silently accumulating. This
// is the no-embedding deployment's behaviour, unchanged by the cosine path.
func TestConsolidatorNoEmbeddingFallsBackToTextFloor(t *testing.T) {
	store := &embeddingStore{
		stubStore: &stubStore{
			facts: []Fact{{ID: "f1", Text: "Alice owns a Honda"}},
			// The pool holds the twin the floor must match against.
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
	if result.Created != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want the near-duplicate caught by the floor", result)
	}
	if len(store.created) != 0 {
		t.Fatalf("a duplicate must not be written: %+v", store.created)
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
