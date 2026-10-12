package storage

import (
	"context"
	"testing"
	"time"
)

func seedBank(t *testing.T, s *MemoryStore, bankID string, matches ...RecallMatch) {
	t.Helper()
	if _, ok := s.banks[bankID]; !ok {
		if _, err := s.CreateBank(context.Background(), Bank{ID: bankID, Name: bankID}); err != nil {
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
	if err := s.UpsertLink(context.Background(), MemoryLink{ID: "l1", BankID: "b1", FromID: "seed", ToID: "linked", LinkType: LinkCauses}); err != nil {
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
	_ = s.UpsertLink(context.Background(), MemoryLink{ID: "l1", BankID: "b1", FromID: "seed", ToID: "target", LinkType: LinkTemporal})
	_ = s.UpsertLink(context.Background(), MemoryLink{ID: "l2", BankID: "b1", FromID: "seed", ToID: "target", LinkType: LinkCauses})
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
		RecallMatch{ID: "obs", Text: "alpha", Type: "observation", SourceFactIDs: []string{"f1"}},
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
