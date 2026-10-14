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
