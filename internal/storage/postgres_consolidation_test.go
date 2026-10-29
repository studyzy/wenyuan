//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
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

// test-consolidation-fetch-order: consolidation works oldest-first, so a
// round's candidates come back in created_at order — the reference sorts its
// recall by created_at ascending (consolidator.py:1459-1461) — not by the
// recency of what they mention.
func TestPostgresConsolidationFactsOldestFirst(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	seeded := []struct {
		label     string
		createdAt time.Time
	}{
		{"order-late", time.Date(2025, 3, 3, 0, 0, 0, 0, time.UTC)},
		{"order-early", time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"order-middle", time.Date(2025, 3, 2, 0, 0, 0, 0, time.UTC)},
	}
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("expected a PostgresStore, got %T", store)
	}
	schema, schemaErr := pg.schemaFor(ctx)
	if schemaErr != nil {
		t.Fatalf("resolve schema: %v", schemaErr)
	}
	for _, unit := range seeded {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: pgUUID(unit.label), BankID: "vec-bank", Text: "text of " + unit.label,
			FactType: TypeExperience,
		}); err != nil {
			t.Fatalf("seed %s: %v", unit.label, err)
		}
		// The unit insert stamps created_at at write (the column default), so
		// the test spreads the seeded rows across the axis the read sorts by.
		if _, err := pg.db.ExecContext(ctx,
			`UPDATE `+quoteIdent(schema)+`.memory_units SET created_at = $2 WHERE id = $1`,
			pgUUID(unit.label), unit.createdAt); err != nil {
			t.Fatalf("backdate %s: %v", unit.label, err)
		}
	}
	facts, err := store.ConsolidationFacts(ctx, "vec-bank", 10)
	if err != nil {
		t.Fatalf("read facts: %v", err)
	}
	var got []string
	for _, fact := range facts {
		switch fact.ID {
		case pgUUID("order-late"), pgUUID("order-early"), pgUUID("order-middle"):
			got = append(got, fact.ID)
		}
	}
	want := []string{pgUUID("order-early"), pgUUID("order-middle"), pgUUID("order-late")}
	if len(got) != len(want) {
		t.Fatalf("facts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("facts = %v, want %v", got, want)
		}
	}
}

// A year-1/9999 placeholder on an observation's own columns reads as unknown
// when the observation is updated: the merged bound is the real date when one
// exists and NULL when only placeholders remain — the reference merges both
// sides through finite_utc (consolidator.py:1376-1380).
func TestPostgresUpdateObservationSkipsPlaceholderBounds(t *testing.T) {
	store := postgresVectorStore(t)
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("expected a PostgresStore, got %T", store)
	}
	ctx := context.Background()
	realEnd := time.Date(2025, 6, 2, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		observationEnd *time.Time
		sourceEnd      *time.Time
		want           *time.Time
	}{
		{
			name:           "placeholder yields to a real date",
			observationEnd: &foreverEnd,
			sourceEnd:      &realEnd,
			want:           &realEnd,
		},
		{
			name:           "placeholder survives no real date",
			observationEnd: &foreverEnd,
			sourceEnd:      nil,
			want:           nil,
		},
		{
			name:           "real bound survives a placeholder",
			observationEnd: &realEnd,
			sourceEnd:      &foreverEnd,
			want:           &realEnd,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observationID := pgUUID("ph-obs-" + test.name)
			sourceID := pgUUID("ph-src-" + test.name)
			if _, err := pg.UpsertUnit(ctx, MemoryUnit{
				ID: observationID, BankID: "vec-bank", Text: "an observation", FactType: TypeObservation,
				OccurredEnd: test.observationEnd,
			}); err != nil {
				t.Fatalf("seed observation: %v", err)
			}
			if _, err := pg.UpsertUnit(ctx, MemoryUnit{
				ID: sourceID, BankID: "vec-bank", Text: "a source fact", FactType: TypeExperience,
				OccurredEnd: test.sourceEnd,
			}); err != nil {
				t.Fatalf("seed source: %v", err)
			}
			if err := pg.UpdateObservation(ctx, "vec-bank", observationID, "an observation, extended", []string{sourceID}); err != nil {
				t.Fatalf("update observation: %v", err)
			}
			bounds, err := pg.sourceObservationBounds(ctx, "vec-bank", []string{observationID})
			if err != nil {
				t.Fatalf("re-read bounds: %v", err)
			}
			switch {
			case test.want == nil && bounds.OccurredEnd != nil:
				t.Fatalf("occurred_end = %v, want NULL", bounds.OccurredEnd)
			case test.want != nil && (bounds.OccurredEnd == nil || !bounds.OccurredEnd.Equal(*test.want)):
				t.Fatalf("occurred_end = %v, want %v", bounds.OccurredEnd, test.want)
			}
		})
	}
}

// foreverEnd is the year-9999 open bound a model writes for "still ongoing".
var foreverEnd = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// A merge folds the merged-away observation's evidence into its survivor: the
// survivor's sources grow, its dates widen with the merged row's, and its text
// is untouched (fold_sources_into_observation).
func TestPostgresMergeObservationEvidenceFolds(t *testing.T) {
	store := postgresVectorStore(t)
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("expected a PostgresStore, got %T", store)
	}
	ctx := context.Background()
	if _, err := pg.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("merge-survivor"), BankID: "vec-bank", Text: "Bob uses open signup", FactType: TypeObservation,
		OccurredStart: at(2025, 6, 2), MentionedAt: at(2025, 6, 2),
	}); err != nil {
		t.Fatalf("seed survivor: %v", err)
	}
	if _, err := pg.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("merge-merged"), BankID: "vec-bank", Text: "Bob is on the beta waitlist", FactType: TypeObservation,
		OccurredStart: at(2024, 1, 15), OccurredEnd: &foreverEnd, MentionedAt: at(2025, 1, 1),
		SourceMemoryIDs: []string{pgUUID("merge-old-fact")},
	}); err != nil {
		t.Fatalf("seed merged: %v", err)
	}
	if _, err := pg.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("merge-old-fact"), BankID: "vec-bank", Text: "the waitlist fact", FactType: TypeExperience,
	}); err != nil {
		t.Fatalf("seed old fact: %v", err)
	}

	if err := pg.MergeObservationEvidence(ctx, "vec-bank", pgUUID("merge-survivor"), pgUUID("merge-merged")); err != nil {
		t.Fatalf("fold: %v", err)
	}
	observations, err := pg.ConsolidationObservations(ctx, "vec-bank", 10)
	if err != nil {
		t.Fatalf("read observations: %v", err)
	}
	var survivor *ConsolidationObservation
	for i := range observations {
		if observations[i].ID == pgUUID("merge-survivor") {
			survivor = &observations[i]
		}
	}
	if survivor == nil {
		t.Fatalf("survivor missing from %+v", observations)
	}
	if survivor.Text != "Bob uses open signup" {
		t.Fatalf("text = %q, want the survivor's own text kept", survivor.Text)
	}
	if survivor.ProofCount != 1 || len(survivor.SourceIDs) != 1 || survivor.SourceIDs[0] != pgUUID("merge-old-fact") {
		t.Fatalf("sources = %v, want the merged evidence folded in", survivor.SourceIDs)
	}
}

// A merge whose survivor is not an observation requeues the merged evidence:
// the live sources' consolidated stamps clear, so the next pass attaches them.
func TestPostgresMergeObservationEvidenceRequeuesWithoutSurvivor(t *testing.T) {
	store := postgresVectorStore(t)
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("expected a PostgresStore, got %T", store)
	}
	ctx := context.Background()
	if _, err := pg.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("requeue-merged"), BankID: "vec-bank", Text: "a merged observation", FactType: TypeObservation,
		SourceMemoryIDs: []string{pgUUID("requeue-old-fact")},
	}); err != nil {
		t.Fatalf("seed merged: %v", err)
	}
	if _, err := pg.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("requeue-old-fact"), BankID: "vec-bank", Text: "the waitlist fact", FactType: TypeExperience,
	}); err != nil {
		t.Fatalf("seed old fact: %v", err)
	}
	if err := pg.MarkConsolidated(ctx, "vec-bank", []string{pgUUID("requeue-old-fact")}); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	// The survivor does not exist: the evidence is requeued, not folded.
	if err := pg.MergeObservationEvidence(ctx, "vec-bank", pgUUID("requeue-missing"), pgUUID("requeue-merged")); err != nil {
		t.Fatalf("fold: %v", err)
	}
	facts, err := pg.ConsolidationFacts(ctx, "vec-bank", 10)
	if err != nil {
		t.Fatalf("read facts: %v", err)
	}
	found := false
	for _, fact := range facts {
		if fact.ID == pgUUID("requeue-old-fact") {
			found = true
		}
	}
	if !found {
		t.Fatalf("requeued fact missing from the candidate read: %+v", facts)
	}
}

// The successor operation of a round-capped chain carries caller_requested
// when a caller chain requeues, and deduplicates against a pending full-bank
// pass — never against the one now running.
func TestPostgresEnqueueConsolidationSuccessor(t *testing.T) {
	store := postgresVectorStore(t)
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("expected a PostgresStore, got %T", store)
	}
	ctx := context.Background()
	bank := "successor-bank"
	if _, err := pg.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	if err := pg.EnqueueConsolidationSuccessor(ctx, bank, true); err != nil {
		t.Fatalf("enqueue successor: %v", err)
	}
	list := func() []Operation {
		t.Helper()
		page, err := pg.ListOperations(ctx, OperationQuery{BankID: bank, Limit: 10})
		if err != nil {
			t.Fatalf("list operations: %v", err)
		}
		return page.Operations
	}
	ops := list()
	if len(ops) != 1 || ops[0].OperationType != OperationTypeConsolidate {
		t.Fatalf("operations = %+v, want one consolidation successor", ops)
	}
	if carried := ops[0].Payload["caller_requested"]; carried != true {
		t.Fatalf("payload = %v, want caller_requested carried", ops[0].Payload)
	}
	// A pending full-bank pass absorbs the requeue; the running one does not.
	if err := pg.EnqueueConsolidationSuccessor(ctx, bank, true); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}
	if ops = list(); len(ops) != 1 {
		t.Fatalf("operations = %d, want the pending pass to absorb the requeue", len(ops))
	}
}
