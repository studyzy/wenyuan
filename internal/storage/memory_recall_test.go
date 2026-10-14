package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seedBank(t *testing.T, s *MemoryStore, bankID string, matches ...RecallMatch) {
	t.Helper()
	if _, ok := s.banks[bankID]; !ok {
		if _, err := s.CreateBank(context.Background(), Bank{BankID: bankID, Name: bankID}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range matches {
		s.SeedMemory(bankID, m)
	}
}

func recallIDs(result RecallResult) []string {
	ids := make([]string, 0, len(result.Matches))
	for _, m := range result.Matches {
		ids = append(ids, m.ID)
	}
	return ids
}

func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// tmg-0001: the result list is ordered by descending final relevance.
func TestRecallOrderedByDescendingRelevance(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "strong", Text: "alpha beta gamma", Type: "world", Entities: []string{"alpha"}},
		RecallMatch{ID: "weak", Text: "alpha", Type: "world"},
	)
	result, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha beta gamma"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(result.Matches); i++ {
		if result.Matches[i-1].Final < result.Matches[i].Final {
			t.Fatalf("results must be ordered by descending final relevance: %v",
				[]float64{result.Matches[0].Final, result.Matches[i].Final})
		}
	}
	if result.Matches[0].ID != "strong" {
		t.Fatalf("the stronger multi-signal result must rank first: %v", recallIDs(result))
	}
}

// beh-0002: a query that matches nothing returns an empty result, not an error.
func TestRecallNoMatchReturnsEmpty(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{ID: "m1", Text: "alpha", Type: "world"})
	result, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "zzz"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("expected empty matches, got %v", recallIDs(result))
	}
}

// beh-0011: overlap modes include untagged memories, strict variants exclude
// them, and set-equality matches the full tag set.
func TestRecallTagMatchModes(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "untagged", Text: "alpha", Type: "world"},
		RecallMatch{ID: "t1", Text: "alpha", Type: "world", Tags: []string{"t1"}},
		RecallMatch{ID: "t2", Text: "alpha", Type: "world", Tags: []string{"t2"}},
	)
	cases := []struct {
		mode string
		want []string
	}{
		{TagsMatchAny, []string{"untagged", "t1"}},
		{TagsMatchAll, []string{"untagged", "t1"}},
		{TagsMatchAnyStrict, []string{"t1"}},
		{TagsMatchAllStrict, []string{"t1"}},
		{TagsMatchExact, []string{"t1"}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			result, err := s.Recall(context.Background(), RecallQuery{
				BankID: "b1", Query: "alpha", Tags: []string{"t1"}, TagsMatch: tc.mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			ids := recallIDs(result)
			if len(ids) != len(tc.want) {
				t.Fatalf("mode %s ids = %v, want %v", tc.mode, ids, tc.want)
			}
			for _, want := range tc.want {
				if !containsID(ids, want) {
					t.Fatalf("mode %s ids = %v, missing %s", tc.mode, ids, want)
				}
			}
		})
	}
}

// beh-0012: compound tag groups AND their leaf and boolean nodes, and a fuzzy
// leaf matches by similarity.
func TestRecallCompoundTagGroups(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "a", Text: "alpha", Type: "world", Tags: []string{"project-x"}},
		RecallMatch{ID: "b", Text: "alpha", Type: "world", Tags: []string{"other"}},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		TagGroups: []TagGroup{{Tag: "project", Resolve: TagResolveFuzzy}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(recallIDs(result), "a") || containsID(recallIDs(result), "b") {
		t.Fatalf("fuzzy leaf match = %v", recallIDs(result))
	}
}

// beh-0017: a bank-disabled arm is omitted entirely and the remaining arms
// still return results.
func TestRecallDisabledArmStillReturns(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{ID: "m1", Text: "alpha", Type: "world"})
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", DisabledArms: []string{ArmSemantic, ArmTemporal, ArmEntity},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) == 0 {
		t.Fatal("remaining keyword arm must still return results")
	}
}

// beh-0018: a punctuation-only query omits the keyword arm but still recalls.
func TestRecallPunctuationQueryOmitsKeywordArm(t *testing.T) {
	s := NewMemoryStore("public")
	mention := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	seedBank(t, s, "b1", RecallMatch{ID: "m1", Text: "alpha", Type: "world", MentionedAt: &mention})
	result, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "!!!"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) == 0 {
		t.Fatal("punctuation-only query must still recall through other arms")
	}
}

// beh-0013: an in-window memory ranks above an out-of-window memory, and the
// out-of-window memory remains reachable.
func TestRecallTemporalWindowRanksInsideFirst(t *testing.T) {
	s := NewMemoryStore("public")
	inside := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	outside := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedBank(t, s, "b1",
		RecallMatch{ID: "outside", Text: "alpha", Type: "world", OccurredStart: &outside},
		RecallMatch{ID: "inside", Text: "alpha", Type: "world", OccurredStart: &inside},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Temporal: &TemporalWindow{Start: inside, End: inside, HasStart: true, HasEnd: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := recallIDs(result)
	if len(ids) != 2 {
		t.Fatalf("out-of-window memory must remain reachable: %v", ids)
	}
	if ids[0] != "inside" {
		t.Fatalf("in-window memory must rank first: %v", ids)
	}
}

// beh-0019: prefer-observations drops raw facts a returned observation
// consolidated from.
func TestRecallPreferObservationsDropsSuperseded(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "raw", Text: "alpha", Type: "world"},
		RecallMatch{ID: "obs", Text: "alpha", Type: "observation", ObservationSourceIDs: []string{"raw"}},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", PreferObservations: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := recallIDs(result)
	if !containsID(ids, "obs") || containsID(ids, "raw") {
		t.Fatalf("raw fact superseded by observation must be dropped: %v", ids)
	}
}

// beh-0019: the flag is a no-op when only raw types are requested.
func TestRecallPreferObservationsNoOpWithoutObservationType(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{ID: "raw", Text: "alpha", Type: "world"})
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Types: []string{"world"}, PreferObservations: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(recallIDs(result), "raw") {
		t.Fatalf("flag must be a no-op without an observation type: %v", recallIDs(result))
	}
}

// beh-0016: a strategy-boosted arm's candidate is promoted.
func TestRecallStrategyBoostPromotes(t *testing.T) {
	s := NewMemoryStore("public")
	// "boosted" matches only via the keyword arm; "plain" also matches.
	seedBank(t, s, "b1",
		RecallMatch{ID: "boosted", Text: "alpha", Type: "world"},
		RecallMatch{ID: "plain", Text: "alpha", Type: "world"},
	)
	base, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	boosted, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", StrategyBoosts: map[string]string{ArmKeyword: "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Matches) != 2 || len(boosted.Matches) != 2 {
		t.Fatalf("boost must not change the result count: base=%v boosted=%v", recallIDs(base), recallIDs(boosted))
	}
}

// neg-0002: a per-arm floor does not act as an intersection.
func TestRecallArmFloorIsNotAnIntersection(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{ID: "m1", Text: "alpha", Type: "world"})
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		MinScores: map[string]float64{"semantic": 0.99},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) == 0 {
		t.Fatal("a result reached through another arm must not be excluded by an unrelated floor")
	}
}

// beh-0014: the temporal arm spreads a score outward through temporal and
// causal links with per-hop decay, so a linked target is reachable.
func TestRecallTemporalSpreadingThroughLinks(t *testing.T) {
	s := NewMemoryStore("public")
	mention := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	seedBank(t, s, "b1",
		RecallMatch{ID: "seed", Text: "alpha", Type: "world", MentionedAt: &mention},
		RecallMatch{ID: "linked", Text: "unrelated text", Type: "world"},
	)
	if err := s.UpsertLink(context.Background(), MemoryLink{BankID: "b1", FromUnitID: "seed", ToUnitID: "linked", LinkType: LinkCauses, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(recallIDs(result), "linked") {
		t.Fatalf("temporal spreading must reach a linked target: %v", recallIDs(result))
	}
}

// beh-0014: spreading keeps the strongest path per target and is bounded.
func TestRecallTemporalSpreadingStrongestPath(t *testing.T) {
	s := NewMemoryStore("public")
	mention := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	seedBank(t, s, "b1",
		RecallMatch{ID: "seed", Text: "alpha", Type: "world", MentionedAt: &mention},
		RecallMatch{ID: "target", Text: "unrelated", Type: "world"},
	)
	// A causal edge propagates further than a temporal edge to the same target.
	_ = s.UpsertLink(context.Background(), MemoryLink{BankID: "b1", FromUnitID: "seed", ToUnitID: "target", LinkType: LinkTemporal, Weight: 1})
	_ = s.UpsertLink(context.Background(), MemoryLink{BankID: "b1", FromUnitID: "seed", ToUnitID: "target", LinkType: LinkCauses, Weight: 1})
	result, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(recallIDs(result), "target") {
		t.Fatalf("strongest path must reach the target: %v", recallIDs(result))
	}
}

// beh-0009: source facts are keyed by the ids the observations cite.
func TestRecallSourceFactChannel(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "obs", Text: "alpha", Type: "observation", SourceMemoryIDs: []string{"f1"}},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Types: []string{"observation"}, IncludeSourceFacts: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.SourceFactMap["f1"]; !ok {
		t.Fatalf("source-fact map must be keyed by cited ids: %+v", result.SourceFactMap)
	}
}

// A configured reranker decides the relevance of the fused pool: the candidate
// it scores highest outranks one the arms alone would have put first.
func TestRecallRerankerOverridesArmRelevance(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "arm-first", Text: "alpha", Type: "world"},
		RecallMatch{ID: "rerank-first", Text: "alpha", Type: "world"},
	)
	calls := 0
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Rerank: func(_ context.Context, _ string, texts []string) ([]float64, error) {
			calls++
			// Score by position in the batch, so whichever candidate the
			// pipeline ordered second is reranked to the top.
			scores := make([]float64, len(texts))
			for i := range scores {
				scores[i] = float64(i)
			}
			return scores, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("reranker called %d times, want once per recall", calls)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("reranker must not drop candidates: %v", recallIDs(result))
	}
	if result.Matches[0].ID != "rerank-first" {
		t.Fatalf("reranked order = %v, want rerank-first at the head", recallIDs(result))
	}
}

// A reranker that fails leaves recall on its fallback rather than failing the
// request, so a flaky cross-encoder degrades quality instead of availability.
func TestRecallRerankerFailureFallsBack(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{ID: "m1", Text: "alpha", Type: "world"})
	fallback, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	rerankDown := errors.New("cross-encoder unavailable")
	failed, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Rerank: func(context.Context, string, []string) ([]float64, error) {
			return nil, rerankDown
		},
	})
	if err != nil {
		t.Fatalf("a reranker failure must not fail the recall: %v", err)
	}
	if len(failed.Matches) != len(fallback.Matches) {
		t.Fatalf("fallback returned %v, failed-rerank returned %v", recallIDs(fallback), recallIDs(failed))
	}
}

// A reranker returning the wrong number of scores is treated as a failure
// rather than paired with the wrong candidate.
func TestRecallRerankerShortResponseFallsBack(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "m1", Text: "alpha", Type: "world"},
		RecallMatch{ID: "m2", Text: "alpha", Type: "world"},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Rerank: func(context.Context, string, []string) ([]float64, error) {
			return []float64{1}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("a short rerank response must not drop candidates: %v", recallIDs(result))
	}
}

// The strategy boost promotes in rank space rather than multiplying the score,
// so its displacement is bounded by the level's divisor: a boosted arm's rank r
// beats another arm's rank s exactly when r < divisor*s, independent of the RRF
// constant and of the pool size.
func TestBoostedRRFPromotionIsRankBased(t *testing.T) {
	semanticRank1 := &recallCandidate{armRanks: map[string]int{ArmSemantic: 1}, armScores: map[string]float64{ArmSemantic: 0.5}}
	keywordRank1 := &recallCandidate{armRanks: map[string]int{ArmKeyword: 1}, armScores: map[string]float64{ArmKeyword: 0.5}}

	fused := func(c *recallCandidate, level string) float64 {
		total := 0.0
		for _, rank := range c.armRanks {
			total += 1 / (rrfConstant + float64(rank))
		}
		return stage1Boost(c, total, map[string]string{ArmKeyword: level})
	}

	// low has divisor 2: keyword rank 1 reaches past semantic rank 1, but
	// keyword rank 3 does not (3 >= 2*1).
	if fused(keywordRank1, "low") <= fused(semanticRank1, "low") {
		t.Fatalf("low: keyword rank 1 %v must beat semantic rank 1 %v",
			fused(keywordRank1, "low"), fused(semanticRank1, "low"))
	}
	keywordRank3 := &recallCandidate{armRanks: map[string]int{ArmKeyword: 3}}
	if fused(keywordRank3, "low") >= fused(semanticRank1, "low") {
		t.Fatalf("low: keyword rank 3 %v must stay below semantic rank 1 %v",
			fused(keywordRank3, "low"), fused(semanticRank1, "low"))
	}

	// high has divisor 8, so the same rank 3 does reach past semantic rank 1:
	// the level, not the constant, decides how far the promotion reaches.
	if fused(keywordRank3, "high") <= fused(semanticRank1, "high") {
		t.Fatalf("high: keyword rank 3 %v must beat semantic rank 1 %v",
			fused(keywordRank3, "high"), fused(semanticRank1, "high"))
	}

	// Without a boost the fused score is the plain RRF sum, so the two arms'
	// rank-1 candidates tie.
	if plain := stage1Boost(keywordRank1, 1/(rrfConstant+1), nil); plain != 1/(rrfConstant+1) {
		t.Fatalf("no boost changed the fused score to %v", plain)
	}
}

// The post-rerank boost decays with the candidate's rank in the boosted arm,
// except for the temporal arm, which is ranked by date rather than relevance.
func TestStage2BoostDecaysExceptTemporal(t *testing.T) {
	boosts := map[string]string{ArmGraph: "high", ArmTemporal: "high"}
	high := &recallCandidate{armRanks: map[string]int{ArmGraph: 1}}
	deep := &recallCandidate{armRanks: map[string]int{ArmGraph: 50}}
	if stage2Boost(high, boosts) <= stage2Boost(deep, boosts) {
		t.Fatalf("graph bump must decay with rank: high=%v deep=%v",
			stage2Boost(high, boosts), stage2Boost(deep, boosts))
	}
	nearTemporal := &recallCandidate{armRanks: map[string]int{ArmTemporal: 1}}
	farTemporal := &recallCandidate{armRanks: map[string]int{ArmTemporal: 50}}
	if stage2Boost(nearTemporal, boosts) != stage2Boost(farTemporal, boosts) {
		t.Fatalf("temporal bump must not decay: near=%v far=%v",
			stage2Boost(nearTemporal, boosts), stage2Boost(farTemporal, boosts))
	}
}

// A boost level that is not one of the three known names contributes nothing,
// so a typo cannot silently promote an arm.
func TestUnknownBoostLevelIsIgnored(t *testing.T) {
	cand := &recallCandidate{armRanks: map[string]int{ArmKeyword: 1}}
	if got := stage2Boost(cand, map[string]string{ArmKeyword: "extreme"}); got != 0 {
		t.Fatalf("unknown boost level contributed %v, want 0", got)
	}
	if got := stage1Boost(cand, 1, map[string]string{ArmKeyword: "extreme"}); got != 1 {
		t.Fatalf("unknown boost level changed the fused score to %v, want 1", got)
	}
}
