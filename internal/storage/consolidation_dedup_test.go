package storage

import (
	"context"
	"testing"
)

// mappingEmbedder returns the vector a test associates with each text, so a
// store can be driven with controlled embedding geometry.
type mappingEmbedder struct {
	byText map[string][]float32
}

func (m *mappingEmbedder) EmbedPassages(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		out[i] = m.byText[text]
	}
	return out, nil
}

func (m *mappingEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return m.byText[text], nil
}

// The near-duplicate probe embeds the candidate text and returns the in-scope
// observations carrying a stored embedding, nearest first, so a caller can
// decide a duplicate by cosine similarity against a threshold.
func TestMemoryObservationDuplicatesFindsNearestTwin(t *testing.T) {
	store := NewMemoryStore("public")
	store.SetObservationEmbedder(&mappingEmbedder{byText: map[string][]float32{
		"alpha": {1, 0},
		"beta":  {0, 1},
	}})
	ctx := context.Background()
	alphaID, err := store.CreateObservation(ctx, "b1", "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateObservation(ctx, "b1", "beta", nil); err != nil {
		t.Fatal(err)
	}

	probe, candidates, embedded, err := store.ObservationDuplicates(ctx, "b1", nil, "alpha", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !embedded {
		t.Fatal("a store with an embedder must report embeddings available")
	}
	if len(probe) != 2 || probe[0] != 1 || probe[1] != 0 {
		t.Fatalf("probe = %v, want the embedding of the candidate text", probe)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want both observations", len(candidates))
	}
	if candidates[0].ID != alphaID {
		t.Fatalf("nearest = %q, want the identical twin %q", candidates[0].ID, alphaID)
	}
	if len(candidates[0].Embedding) != 2 || candidates[0].Embedding[0] != 1 {
		t.Fatalf("candidate embedding = %v, want the stored vector", candidates[0].Embedding)
	}
}

// A deployment with no embedding model cannot probe: the store reports
// embeddings unavailable so the caller falls back to its text-similarity floor
// rather than silently losing dedup.
func TestMemoryObservationDuplicatesWithoutEmbedder(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateObservation(ctx, "b1", "alpha", nil); err != nil {
		t.Fatal(err)
	}
	_, candidates, embedded, err := store.ObservationDuplicates(ctx, "b1", nil, "alpha", 5)
	if err != nil {
		t.Fatal(err)
	}
	if embedded {
		t.Fatal("a store with no embedder must report embeddings unavailable")
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
}

// Only observations of the pass's scope are probed, so a twin cannot be found
// across a scope boundary.
func TestMemoryObservationDuplicatesRespectsScope(t *testing.T) {
	store := NewMemoryStore("public")
	store.SetObservationEmbedder(&mappingEmbedder{byText: map[string][]float32{
		"alpha": {1, 0},
		"gamma": {1, 0},
	}})
	ctx := context.Background()
	untagged, err := store.CreateObservation(ctx, "b1", "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateObservationInScope(ctx, "b1", "gamma", nil, []string{"x"}); err != nil {
		t.Fatal(err)
	}

	_, candidates, embedded, err := store.ObservationDuplicates(ctx, "b1", nil, "alpha", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !embedded || len(candidates) != 1 || candidates[0].ID != untagged {
		t.Fatalf("candidates = %+v, want only the untagged observation %q", candidates, untagged)
	}
}
