package storage

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

// ---- T1: temporal independent retrieval arm ----

// Coverage selection spreads ten entry points across the eight time buckets of
// the window instead of taking the ten strongest rows from one bucket.
func TestSelectTemporalCoverageSpreadsAcrossBuckets(t *testing.T) {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 8)
	pool := make([]temporalRow, 0, 24)
	for b := 0; b < 8; b++ {
		for k := 0; k < 3; k++ {
			date := start.AddDate(0, 0, b)
			similarity := 1.0 - float64(b)/100 - float64(k)/1000
			pool = append(pool, temporalRow{
				match:      RecallMatch{ID: fmt.Sprintf("b%d-k%d", b, k), Type: "world", OccurredStart: &date},
				similarity: similarity,
			})
		}
	}
	got := selectTemporalCoverage(pool, start, end, temporalEntryPoints, temporalCoverageBuckets)
	if len(got) != temporalEntryPoints {
		t.Fatalf("coverage selected %d entry points, want %d", len(got), temporalEntryPoints)
	}
	seen := map[int]bool{}
	for _, row := range got {
		bucket := int(row.match.OccurredStart.Sub(start).Hours() / 24)
		seen[bucket] = true
	}
	if len(seen) != temporalCoverageBuckets {
		t.Fatalf("entry points covered %d buckets, want all %d: %v", len(seen), temporalCoverageBuckets, seen)
	}
}

// When every date lands in one bucket, coverage collapses to plain similarity
// order.
func TestSelectTemporalCoverageSingleBucketIsSimilarityOrder(t *testing.T) {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 8)
	date := start
	pool := []temporalRow{
		{match: RecallMatch{ID: "low", OccurredStart: &date}, similarity: 0.2},
		{match: RecallMatch{ID: "high", OccurredStart: &date}, similarity: 0.9},
		{match: RecallMatch{ID: "mid", OccurredStart: &date}, similarity: 0.5},
	}
	got := selectTemporalCoverage(pool, start, end, 2, temporalCoverageBuckets)
	if len(got) != 2 || got[0].match.ID != "high" || got[1].match.ID != "mid" {
		t.Fatalf("single-bucket coverage = %v, want [high mid] in similarity order", []string{got[0].match.ID, got[1].match.ID})
	}
}

// A hop propagates the parent's temporal score scaled by the stored weight, the
// causal boost and one hop of decay: 1.0 * 1.0 * 2.0 * 0.7 = 1.4.
func TestTemporalSpreadUsesDecayAndCausalBoost(t *testing.T) {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 30)
	mid := start.AddDate(0, 0, 15)
	entry := temporalRow{match: RecallMatch{ID: "seed", Type: "world", OccurredStart: &mid}, similarity: 1}
	target := temporalRow{match: RecallMatch{ID: "target", Type: "world"}, similarity: 1}
	links := []temporalLink{{from: "seed", weight: 1, kind: LinkCauses, target: target}}
	neighbors := func(_ []string) []temporalLink { return links }
	hits := spreadTemporalArm([]temporalRow{entry}, start, end, 10, "world", neighbors)
	for _, hit := range hits {
		if hit.match.ID != "target" {
			continue
		}
		if math.Abs(*hit.temporalScore-1.4) > 1e-9 {
			t.Fatalf("propagated temporal score = %v, want 1.4", *hit.temporalScore)
		}
		if math.Abs(*hit.temporalProximity-0.3) > 1e-9 {
			t.Fatalf("undated neighbour proximity = %v, want 0.3", *hit.temporalProximity)
		}
		return
	}
	t.Fatal("the causal link did not reach its target")
}

// The walk is capped at five hops: a chain longer than that stops before the
// sixth node regardless of the node budget.
func TestTemporalSpreadStopsAtFiveHops(t *testing.T) {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 30)
	entry := temporalRow{match: RecallMatch{ID: "n0", Type: "world"}, similarity: 1}
	links := make([]temporalLink, 0, 7)
	for i := 1; i <= 7; i++ {
		links = append(links, temporalLink{
			from:   fmt.Sprintf("n%d", i-1),
			weight: 1,
			kind:   LinkTemporal,
			target: temporalRow{match: RecallMatch{ID: fmt.Sprintf("n%d", i), Type: "world"}, similarity: 1},
		})
	}
	neighbors := func(fromIDs []string) []temporalLink {
		set := map[string]bool{}
		for _, id := range fromIDs {
			set[id] = true
		}
		out := make([]temporalLink, 0)
		for _, l := range links {
			if set[l.from] {
				out = append(out, l)
			}
		}
		return out
	}
	hits := spreadTemporalArm([]temporalRow{entry}, start, end, 100, "world", neighbors)
	// One entry point plus one node per hop, five hops.
	if len(hits) != temporalMaxHops+1 {
		t.Fatalf("spread emitted %d results, want %d (one entry + %d hops)", len(hits), temporalMaxHops+1, temporalMaxHops)
	}
	for _, hit := range hits {
		if hit.match.ID == "n6" {
			t.Fatal("the sixth hop must not be reached")
		}
	}
}

// A temporal hit carries the propagated score and window proximity alongside
// its ANN similarity, so both reach fusion.
func TestTemporalHitCarriesScores(t *testing.T) {
	hit := temporalArmHit(temporalRow{match: RecallMatch{ID: "x"}, similarity: 0.8}, 0.6, 0.4)
	if hit.score != 0.8 || hit.temporalScore == nil || *hit.temporalScore != 0.6 || hit.temporalProximity == nil || *hit.temporalProximity != 0.4 {
		t.Fatalf("temporal hit = %+v, want similarity 0.8, temporal 0.6, proximity 0.4", hit)
	}
}

// A bounded window makes the temporal arm filter its own pool: a unit dated
// outside the window is no longer surfaced by the temporal arm (it remains
// reachable through the other arms).
func TestTemporalWindowArmFiltersPoolToWindow(t *testing.T) {
	s := NewMemoryStore("public")
	inside := time.Date(2020, 1, 5, 0, 0, 0, 0, time.UTC)
	outside := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	seedBank(t, s, "w2t",
		RecallMatch{ID: "outside", Text: "alpha", Type: "world", OccurredStart: &outside, MentionedAt: &outside},
		RecallMatch{ID: "inside", Text: "alpha", Type: "world", OccurredStart: &inside, MentionedAt: &inside},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "w2t", Query: "alpha",
		Temporal: &TemporalWindow{
			Start:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
			End:      time.Date(2020, 1, 9, 0, 0, 0, 0, time.UTC),
			HasStart: true, HasEnd: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var outMatch *RecallMatch
	for i := range result.Matches {
		if result.Matches[i].ID == "outside" {
			outMatch = &result.Matches[i]
		}
	}
	if outMatch == nil {
		t.Fatal("the out-of-window unit must remain reachable through other arms")
	}
	if _, temporal := outMatch.ArmRanks[ArmTemporal]; temporal {
		t.Fatalf("the out-of-window unit must not be surfaced by the temporal arm: %v", outMatch.ArmRanks)
	}
}

// ---- R2: token-filter candidate bound is twice the thinking budget ----

func TestTokenFilterCandidateLimitDoublesThinkingBudget(t *testing.T) {
	if got := tokenFilterCandidateLimit(RecallQuery{}, 100); got != 200 {
		t.Fatalf("no explicit limit: bound = %d, want 200", got)
	}
	if got := tokenFilterCandidateLimit(RecallQuery{Limit: 3}, 100); got != 3 {
		t.Fatalf("explicit limit must stay a strict cap: bound = %d, want 3", got)
	}
}

func TestRecallDoublesThinkingBudgetBeforeTokenFilter(t *testing.T) {
	s := NewMemoryStore("public")
	if _, err := s.CreateBank(context.TODO(), Bank{BankID: "w2t-r2", Name: "w2t-r2"}); err != nil {
		t.Fatal(err)
	}
	const written = 2*BudgetLowLimit + 50
	items := make([]RetainItem, 0, written)
	for i := 0; i < written; i++ {
		items = append(items, RetainItem{Content: fmt.Sprintf("fact number %d about the topic", i), DocumentID: fmt.Sprintf("d%d", i)})
	}
	if _, err := s.RetainWrite(context.TODO(), RetainRequest{BankID: "w2t-r2", Items: items}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Recall(context.TODO(), RecallQuery{BankID: "w2t-r2", Query: "fact topic", Budget: BudgetLow, MaxTokens: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2*BudgetLowLimit {
		t.Fatalf("recall returned %d matches, want twice the low bound (%d)", len(result.Matches), 2*BudgetLowLimit)
	}
}

// ---- R3: rrf_normalized is always 0.0 ----

func TestRRFNormalizedIsAlwaysZero(t *testing.T) {
	list := []*recallCandidate{
		{components: map[string]float64{"rrf_score": 0.1}},
		{components: map[string]float64{"rrf_score": 0.9}},
	}
	normalizeRRFComponent(list)
	for i, cand := range list {
		if got := cand.components["rrf_normalized"]; got != 0 {
			t.Fatalf("candidate %d rrf_normalized = %v, want 0", i, got)
		}
	}
}

// ---- R5: proof signal from the stored count, non-observations neutral ----

func TestCombinedSignalsProofFromStoredCount(t *testing.T) {
	cand := &recallCandidate{
		match:      RecallMatch{ID: "o", Type: string(TypeObservation), SourceMemoryIDs: []string{"s1"}},
		proofCount: 5,
	}
	_, _, proof := combinedSignals(cand, RecallQuery{})
	want := 0.5 + math.Log(5)/10
	if math.Abs(proof-want) > 1e-9 {
		t.Fatalf("proof signal = %v, want the stored count's %v", proof, want)
	}
}

func TestCombinedSignalsProofNeutralForNonObservation(t *testing.T) {
	cand := &recallCandidate{
		match:      RecallMatch{ID: "f", Type: "world", SourceMemoryIDs: []string{"s1", "s2"}},
		proofCount: 5,
	}
	_, _, proof := combinedSignals(cand, RecallQuery{})
	if proof != 0.5 {
		t.Fatalf("a non-observation proof signal = %v, want neutral 0.5", proof)
	}
}

// ---- T3: fuzzy expansion order and case folding ----

// Expansion returns the closest tags first, not in vocabulary order.
func TestExpandTagTokenSortedBySimilarity(t *testing.T) {
	vocabulary := []string{"ns:abcde", "ns:abcdef", "ns:abcdefg", "ns:abcdefghi"}
	got := expandTagToken("ns:abcdefgh", vocabulary)
	if len(got) != len(vocabulary) {
		t.Fatalf("expansion = %v, want all %d near tags", got, len(vocabulary))
	}
	_, tokenValue := splitNamespace(caseFoldTagToken("ns:abcdefgh"))
	for i := 0; i+1 < len(got); i++ {
		_, vi := splitNamespace(caseFoldTagToken(got[i]))
		_, vj := splitNamespace(caseFoldTagToken(got[i+1]))
		if trigramJaccard(tokenValue, vi) < trigramJaccard(tokenValue, vj) {
			t.Fatalf("expansion is not similarity-descending at %d: %v", i, got)
		}
	}
	if got[0] != "ns:abcdefghi" {
		t.Fatalf("closest tag must lead, got %v", got)
	}
}

// Case folding (not simple lowering) makes a sharp-s token match the folded
// spelling of a tag as an exact hit.
func TestExpandTagTokenCaseFoldExactHit(t *testing.T) {
	got := expandTagToken("name:groß", []string{"name:GROSS"})
	if len(got) != 1 || got[0] != "name:GROSS" {
		t.Fatalf("casefold expansion = %v, want the exact tag [name:GROSS]", got)
	}
}
