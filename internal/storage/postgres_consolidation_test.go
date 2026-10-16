//go:build pgx

package storage

import (
	"context"
	"testing"
)

// test-consolidation-round-trip: a consolidation pass reads the facts that are
// not yet folded, writes an observation linked to its sources, and marks the
// facts consumed so the next pass does not reconsider them.
func TestConsolidationRoundTrip(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	for _, id := range []string{pgUUID("fact-1"), pgUUID("fact-2")} {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: id, BankID: "vec-bank", Text: "text of " + id, FactType: TypeExperience,
		}); err != nil {
			t.Fatalf("seed fact %s: %v", id, err)
		}
	}

	facts, err := store.ConsolidationFacts(ctx, "vec-bank", 10)
	if err != nil {
		t.Fatalf("read facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(facts))
	}

	observationID, err := store.CreateObservation(ctx, "vec-bank", "a consolidated conclusion",
		[]string{pgUUID("fact-1"), pgUUID("fact-2")})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}

	observations, err := store.ConsolidationObservations(ctx, "vec-bank", 10)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(observations))
	}
	if observations[0].ID != observationID || observations[0].ProofCount != 2 {
		t.Fatalf("observation = %+v, want id %q with two sources", observations[0], observationID)
	}

	if err := store.MarkConsolidated(ctx, "vec-bank", []string{pgUUID("fact-1"), pgUUID("fact-2")}); err != nil {
		t.Fatalf("mark consolidated: %v", err)
	}
	facts, err = store.ConsolidationFacts(ctx, "vec-bank", 10)
	if err != nil {
		t.Fatalf("re-read facts: %v", err)
	}
	if len(facts) != 0 {
		t.Fatalf("facts after marking = %d, want 0", len(facts))
	}

	// An update rewrites the observation and attaches more evidence.
	if err := store.UpdateObservation(ctx, "vec-bank", observationID, "a revised conclusion", []string{pgUUID("fact-1")}); err != nil {
		t.Fatalf("update observation: %v", err)
	}
	observations, err = store.ConsolidationObservations(ctx, "vec-bank", 10)
	if err != nil {
		t.Fatalf("re-read observations: %v", err)
	}
	if observations[0].Text != "a revised conclusion" {
		t.Fatalf("text = %q, want the revision", observations[0].Text)
	}

	// A delete removes the observation and its evidence links.
	if err := store.DeleteObservation(ctx, "vec-bank", observationID); err != nil {
		t.Fatalf("delete observation: %v", err)
	}
	observations, err = store.ConsolidationObservations(ctx, "vec-bank", 10)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if len(observations) != 0 {
		t.Fatalf("observations after delete = %d, want 0", len(observations))
	}
}

// The near-duplicate probe embeds the candidate text and returns the in-scope
// observations carrying a stored vector, nearest first: the vector index's
// cosine distance decides which twin a create is checked against.
func TestPostgresObservationDuplicatesFindsNearestTwin(t *testing.T) {
	store := postgresVectorStore(t)
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("expected a PostgresStore, got %T", store)
	}
	pg.SetObservationEmbedder(&mappingEmbedder{byText: map[string][]float32{
		"alpha": unitVector(0),
		"beta":  unitVector(1),
	}})
	ctx := context.Background()

	alphaID, err := pg.CreateObservation(ctx, "vec-bank", "alpha", nil)
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if _, err := pg.CreateObservation(ctx, "vec-bank", "beta", nil); err != nil {
		t.Fatalf("create beta: %v", err)
	}

	probe, candidates, embedded, err := pg.ObservationDuplicates(ctx, "vec-bank", nil, "alpha", 5)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !embedded {
		t.Fatal("a store with an embedder must report embeddings available")
	}
	if len(probe) != EmbeddingDimensions || probe[0] != 1 {
		t.Fatalf("probe = %v, want the embedding of the candidate text", probe)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want both observations", len(candidates))
	}
	if candidates[0].ID != alphaID {
		t.Fatalf("nearest = %q, want the identical twin %q", candidates[0].ID, alphaID)
	}
	if len(candidates[0].Embedding) != EmbeddingDimensions || candidates[0].Embedding[0] != 1 {
		t.Fatalf("candidate embedding = %v, want the stored vector", candidates[0].Embedding)
	}
}

// A store with no embedding model reports embeddings unavailable, so the caller
// falls back to its text-similarity floor rather than losing dedup.
func TestPostgresObservationDuplicatesWithoutEmbedder(t *testing.T) {
	store := postgresVectorStore(t)
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("expected a PostgresStore, got %T", store)
	}
	ctx := context.Background()
	if _, err := pg.CreateObservation(ctx, "vec-bank", "alpha", nil); err != nil {
		t.Fatalf("create observation: %v", err)
	}
	_, candidates, embedded, err := pg.ObservationDuplicates(ctx, "vec-bank", nil, "alpha", 5)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if embedded {
		t.Fatal("a store with no embedder must report embeddings unavailable")
	}
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v, want none", candidates)
	}
}
