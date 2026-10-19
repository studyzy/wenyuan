package storage

import (
	"context"
	"math"
	"testing"
)

// graphHitScore returns the score an arm gave one identifier, if it surfaced it.
func graphHitScore(hits []recallHit, id string) (float64, bool) {
	for _, hit := range hits {
		if hit.match.ID == id {
			return hit.score, true
		}
	}
	return 0, false
}

// An entity co-occurrence hop reaches a unit that shares an entity with a seed
// even when no explicit link connects them, weighted by the upstream saturation
// curve.
func TestGraphArmEntityCooccurrenceReachesUnlinkedUnit(t *testing.T) {
	store := NewMemoryStore("public")
	seedBank(t, store, "b1",
		RecallMatch{ID: "seed", Text: "alice works at google", Type: "world"},
		RecallMatch{ID: "shared", Text: "the campus is in mountain view", Type: "world"},
		RecallMatch{ID: "other", Text: "unrelated note about weather", Type: "world"},
	)
	ctx := context.Background()
	// The seed and the shared unit mention the same entity; the third mentions
	// nothing the seed does.
	if err := store.UpsertUnitEntity(ctx, "seed", "ent-acme"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertUnitEntity(ctx, "shared", "ent-acme"); err != nil {
		t.Fatal(err)
	}

	hits := store.runGraphArm(RecallQuery{BankID: "b1", Query: "alice google"}, []string{"world"}, "alice google")
	score, ok := graphHitScore(hits, "shared")
	if !ok {
		t.Fatalf("the shared-entity unit was not reached without a link: %+v", hits)
	}
	if want := math.Tanh(graphEntitySaturation); math.Abs(score-want) > 1e-9 {
		t.Fatalf("entity-cooccurrence weight = %v, want tanh(0.5) = %v", score, want)
	}
	if _, ok := graphHitScore(hits, "other"); ok {
		t.Fatal("a unit sharing no entity with a seed must not be reached")
	}
}

// A unit reached by both a stored link and an entity co-occurrence carries the
// sum of the two weights, so convergent evidence outranks either signal alone.
func TestGraphArmAddsEntityAndLinkWeights(t *testing.T) {
	store := NewMemoryStore("public")
	seedBank(t, store, "b1",
		RecallMatch{ID: "seed", Text: "alice works at google", Type: "world"},
		RecallMatch{ID: "linkOnly", Text: "campus one", Type: "world"},
		RecallMatch{ID: "entityOnly", Text: "campus two", Type: "world"},
		RecallMatch{ID: "both", Text: "campus three", Type: "world"},
	)
	ctx := context.Background()
	for _, target := range []string{"linkOnly", "both"} {
		if err := store.UpsertLink(ctx, MemoryLink{
			BankID: "b1", FromUnitID: "seed", ToUnitID: target, LinkType: LinkSemantic, Weight: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, unit := range []string{"seed", "entityOnly", "both"} {
		if err := store.UpsertUnitEntity(ctx, unit, "e1"); err != nil {
			t.Fatal(err)
		}
	}

	hits := store.runGraphArm(RecallQuery{BankID: "b1", Query: "alice google"}, []string{"world"}, "alice google")
	linkScore, _ := graphHitScore(hits, "linkOnly")
	entityScore, _ := graphHitScore(hits, "entityOnly")
	bothScore, _ := graphHitScore(hits, "both")
	if bothScore <= linkScore || bothScore <= entityScore {
		t.Fatalf("combined weight %v must exceed link-only %v and entity-only %v", bothScore, linkScore, entityScore)
	}
	if want := linkScore + entityScore; math.Abs(bothScore-want) > 1e-9 {
		t.Fatalf("combined weight = %v, want link + entity = %v", bothScore, want)
	}
}

// The graph arm's hop weight is additive across link kinds: a causes link
// propagates further than a temporal one, and a pair joined by both carries the
// sum.
func TestHopWeightIsAdditiveAcrossLinkKinds(t *testing.T) {
	causes := hopWeight([]MemoryLinkType{LinkCauses})
	temporal := hopWeight([]MemoryLinkType{LinkTemporal})
	if causes <= temporal {
		t.Fatalf("a causes hop (%v) must outweigh a temporal one (%v)", causes, temporal)
	}
	combined := hopWeight([]MemoryLinkType{LinkCauses, LinkTemporal})
	if combined <= causes || combined <= temporal {
		t.Fatalf("a hop that is both causal and temporal (%v) must reflect both (%v, %v)", combined, causes, temporal)
	}
	if want := causes + temporal; math.Abs(combined-want) > 1e-9 {
		t.Fatalf("combined hop weight = %v, want causes + temporal = %v", combined, want)
	}
}

// The scoring path agrees: a unit reached over a causes link scores above one
// reached over a temporal link from the same seed.
func TestGraphArmCausalHopOutweighsTemporalHop(t *testing.T) {
	store := NewMemoryStore("public")
	seedBank(t, store, "b1",
		RecallMatch{ID: "seed", Text: "alice works at google", Type: "world"},
		RecallMatch{ID: "caused", Text: "campus one", Type: "world"},
		RecallMatch{ID: "timed", Text: "campus two", Type: "world"},
	)
	ctx := context.Background()
	if err := store.UpsertLink(ctx, MemoryLink{
		BankID: "b1", FromUnitID: "seed", ToUnitID: "caused", LinkType: LinkCauses, Weight: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertLink(ctx, MemoryLink{
		BankID: "b1", FromUnitID: "seed", ToUnitID: "timed", LinkType: LinkTemporal, Weight: 1,
	}); err != nil {
		t.Fatal(err)
	}
	hits := store.runGraphArm(RecallQuery{BankID: "b1", Query: "alice google"}, []string{"world"}, "alice google")
	caused, _ := graphHitScore(hits, "caused")
	timed, _ := graphHitScore(hits, "timed")
	if caused <= timed {
		t.Fatalf("a causes hop (%v) must propagate further than a temporal one (%v)", caused, timed)
	}
}

// Only the strongest dense hits seed the walk, so a broad tail does not fan it
// out. A hit below the configured seed floor never seeds.
func TestGraphSeedsPickStrongestDenseHits(t *testing.T) {
	hits := make([]recallHit, 0, graphSeedLimit+5)
	for i := range graphSeedLimit + 5 {
		hits = append(hits, recallHit{match: RecallMatch{ID: idFor(i)}, score: float64(i + 1)})
	}
	ids := graphSeedsFrom(hits, 0.3)
	if len(ids) != graphSeedLimit {
		t.Fatalf("seeded %d candidates, want %d", len(ids), graphSeedLimit)
	}
	if containsID(ids, idFor(0)) {
		t.Fatal("the weakest candidate must be dropped by the seed cap")
	}
	if !containsID(ids, idFor(graphSeedLimit+4)) {
		t.Fatal("the strongest candidate must seed the walk")
	}
}

// A dense hit below the arm's seed floor is not an entry point, and a candidate
// the dense arm did not surface cannot seed at all: there is no lexical
// fallback, matching the reference.
func TestGraphSeedsClearTheFloorAndComeOnlyFromDenseHits(t *testing.T) {
	hits := []recallHit{
		{match: RecallMatch{ID: "strong"}, score: 0.9},
		{match: RecallMatch{ID: "weak"}, score: 0.1},
	}
	ids := graphSeedsFrom(hits, 0.3)
	if !containsID(ids, "strong") {
		t.Fatalf("a dense hit above the floor must seed the walk: %v", ids)
	}
	if containsID(ids, "weak") {
		t.Fatalf("a dense hit below the floor must not seed: %v", ids)
	}
	if got := graphSeedsFrom(nil, 0.3); len(got) != 0 {
		t.Fatalf("a query with no dense hits must have no seeds: %v", got)
	}
}

// The in-process seed set applies the same floor and cap as the PostgreSQL arm,
// so both backends start from the strongest handful.
func TestMemoryGraphSeedsAreCapped(t *testing.T) {
	store := NewMemoryStore("public")
	matches := make([]RecallMatch, 0, graphSeedLimit+5)
	for i := range graphSeedLimit + 5 {
		matches = append(matches, RecallMatch{ID: idFor(i), Text: "alice google", Type: "world"})
	}
	seedBank(t, store, "b1", matches...)
	seeds := store.graphSeeds(RecallQuery{BankID: "b1", Query: "alice google"}, []string{"world"}, "alice google")
	if len(seeds) != graphSeedLimit {
		t.Fatalf("seeded %d, want %d", len(seeds), graphSeedLimit)
	}
}

// A memory that fails the dense arm's seed floor is not an entry point, even
// though the dense arm would still report it above zero: the memory backend
// applies the same floor as PostgreSQL.
func TestMemoryGraphSeedsRequireTheDenseFloor(t *testing.T) {
	store := NewMemoryStore("public")
	// One of the query's four words, so the dense stand-in scores 0.25 — below
	// the 0.3 seed floor.
	seedBank(t, store, "b1", RecallMatch{ID: "half-alice", Text: "alice", Type: "world"})
	seeds := store.graphSeeds(
		RecallQuery{BankID: "b1", Query: "alice google campus annex"},
		[]string{"world"}, "alice google campus annex")
	if len(seeds) != 0 {
		t.Fatalf("a memory below the dense seed floor must not seed the walk: %v", seeds)
	}
}
