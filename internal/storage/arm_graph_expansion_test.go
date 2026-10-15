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

// Only the strongest candidates seed the walk, so a broad lexical tail does not
// fan it out. The similarity is the semantic score when present, else the
// keyword score; a non-positive score never seeds.
func TestGraphSeedIDsPicksStrongestBySimilarity(t *testing.T) {
	candidates := map[string]*recallCandidate{}
	for i := range graphSeedLimit + 5 {
		candidates[idFor(i)] = &recallCandidate{armScores: map[string]float64{ArmSemantic: float64(i + 1)}}
	}
	ids := graphSeedIDs(candidates)
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

func TestGraphSeedIDsFallsBackToKeywordAndDropsZeroScores(t *testing.T) {
	candidates := map[string]*recallCandidate{
		"keyword": {armScores: map[string]float64{ArmKeyword: 2}},
		"zero":    {armScores: map[string]float64{}},
	}
	ids := graphSeedIDs(candidates)
	if !containsID(ids, "keyword") {
		t.Fatalf("a keyword-only candidate must seed the walk offline: %v", ids)
	}
	if containsID(ids, "zero") {
		t.Fatalf("a candidate with no positive similarity must not seed: %v", ids)
	}
}

// The in-process seed set is capped the same way, so both backends start from
// the strongest handful.
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
