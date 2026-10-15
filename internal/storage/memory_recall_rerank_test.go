package storage

import (
	"context"
	"errors"
	"math"
	"testing"
)

// A reranker that scores part of its batch before failing hands those scores
// back; the scored candidate takes the reranked base, the unscored one keeps
// its fused score rather than the whole recall falling back.
func TestRecallRerankerPartialKeepsFusedTail(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "a", Text: "alpha", Type: "world"},
		RecallMatch{ID: "b", Text: "alpha", Type: "world"},
	)
	// The pool is ordered by id, so the reranker sees a then b; it scores only
	// b (the second) and marks a unscored with a NaN. Without the partial path
	// the whole pool would fall back and a would stay first on the id tie-break.
	baseline, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	fusedFinal := map[string]float64{}
	for _, m := range baseline.Matches {
		fusedFinal[m.ID] = m.Final
	}

	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Rerank: func(_ context.Context, _ string, texts []string) ([]float64, error) {
			scores := make([]float64, len(texts))
			for i := range scores {
				scores[i] = math.NaN()
			}
			scores[len(scores)-1] = 0.9
			return nil, &PartialRerankError{Scores: scores}
		},
	})
	if err != nil {
		t.Fatalf("a partial rerank must not fail the recall: %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("partial rerank dropped candidates: %v", recallIDs(result))
	}
	if result.Matches[0].ID != "b" {
		t.Fatalf("the scored candidate must rank first: %v", recallIDs(result))
	}
	for _, m := range result.Matches {
		if m.ID != "a" {
			continue
		}
		if m.Final != fusedFinal["a"] {
			t.Fatalf("an unscored candidate must keep its fused score: got %v, want %v", m.Final, fusedFinal["a"])
		}
	}
}

// A partial error whose score count does not match the batch is a total
// failure, so the recall falls back throughout rather than mis-pairing scores.
func TestRecallRerankerPartialWrongLengthFallsBack(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "a", Text: "alpha", Type: "world"},
		RecallMatch{ID: "b", Text: "alpha", Type: "world"},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Rerank: func(context.Context, string, []string) ([]float64, error) {
			return nil, &PartialRerankError{Scores: []float64{0.5}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("a mis-sized partial result must not drop candidates: %v", recallIDs(result))
	}
}

// A candidate past the rerank depth keeps its fused score, and the full pool is
// still returned: the shallow-rerank case that used to mis-index the score slice.
func TestRecallRerankDepthKeepsTailFused(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "a", Text: "alpha", Type: "world"},
		RecallMatch{ID: "b", Text: "alpha", Type: "world"},
		RecallMatch{ID: "c", Text: "alpha", Type: "world"},
	)
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", RerankDepth: 1,
		Rerank: func(_ context.Context, _ string, texts []string) ([]float64, error) {
			scores := make([]float64, len(texts))
			for i := range scores {
				scores[i] = 1
			}
			return scores, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 3 {
		t.Fatalf("rerank depth must not drop candidates: %v", recallIDs(result))
	}
}

// passthroughRerankSeed maps the descending fused order onto [1.0, 0.1]:
// strongest first, weakest last, independent of the reranker's own values.
func TestPassthroughRerankSeedFollowsFusedOrder(t *testing.T) {
	strong := &recallCandidate{match: RecallMatch{ID: "strong"}, armRanks: map[string]int{ArmSemantic: 1}}
	weak := &recallCandidate{match: RecallMatch{ID: "weak"}, armRanks: map[string]int{ArmSemantic: 5}}
	seed := passthroughRerankSeed([]*recallCandidate{strong, weak}, []string{ArmSemantic})
	if seed[0] != 1 || math.Abs(seed[1]-0.1) > 1e-9 {
		t.Fatalf("seed = %v, want [1 0.1] by fused order", seed)
	}
	// A single candidate is seeded at the top of the range.
	single := passthroughRerankSeed([]*recallCandidate{strong}, []string{ArmSemantic})
	if len(single) != 1 || single[0] != 1 {
		t.Fatalf("single-candidate seed = %v, want [1]", single)
	}
}

// A passthrough reranker's raw positions would invert the fused order under
// min-max normalisation; with RerankSeedFromFused set the fused order is seeded
// and preserved instead.
func TestSeedFromFusedRerankerKeepsFusedOrder(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "a", Text: "alpha", Type: "world"},
		RecallMatch{ID: "b", Text: "alpha", Type: "world"},
	)
	rerank := func(_ context.Context, _ string, texts []string) ([]float64, error) {
		// Echo position: the second candidate scores higher.
		scores := make([]float64, len(texts))
		for i := range scores {
			scores[i] = float64(i)
		}
		return scores, nil
	}
	// "a" has the stronger fused rank, so seeding must keep it first.
	seeded, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Rerank: rerank, RerankSeedFromFused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seeded.Matches[0].ID != "a" {
		t.Fatalf("seeded rank-position rerank must keep the fused order: %v", recallIDs(seeded))
	}
	// Without the flag the raw positions are normalised and invert the order,
	// which is the behaviour the seeding replaces.
	normalized, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Rerank: rerank,
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Matches[0].ID != "b" {
		t.Fatalf("unseeded normalisation should invert the order: %v", recallIDs(normalized))
	}
}

// A plain error still falls back throughout: only the partial error opts into
// the keep-the-tail behaviour.
func TestRecallRerankerHardErrorStillFallsBack(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{ID: "a", Text: "alpha", Type: "world"})
	if _, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Rerank: func(context.Context, string, []string) ([]float64, error) {
			return nil, errors.New("boom")
		},
	}); err != nil {
		t.Fatalf("a reranker error must not fail recall: %v", err)
	}
}

// A pruning reranker marks a candidate it would remove with a score of exactly
// 0.0; with RerankPrunesCandidates set those candidates are dropped from the
// result rather than carried to the token budget. Without the flag the same
// scores are ordering only, so nothing is dropped.
func TestPruningRerankerDropsZeroScoredCandidates(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "a", Text: "alpha", Type: "world"},
		RecallMatch{ID: "b", Text: "alpha", Type: "world"},
		RecallMatch{ID: "c", Text: "alpha", Type: "world"},
	)
	// The pool is ordered by id, so the reranker sees a, b, c. It judges only
	// a relevant; b and c score exactly 0.0.
	rerank := func(_ context.Context, _ string, texts []string) ([]float64, error) {
		scores := make([]float64, len(texts))
		scores[0] = 1.0
		return scores, nil
	}

	pruned, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Rerank: rerank, RerankPrunesCandidates: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned.Matches) != 1 || pruned.Matches[0].ID != "a" {
		t.Fatalf("a pruning reranker must keep only the scored candidate: %v", recallIDs(pruned))
	}

	kept, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Rerank: rerank,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Matches) != 3 {
		t.Fatalf("an ordering reranker must not drop candidates: %v", recallIDs(kept))
	}
}

// An unscored candidate (NaN) is never pruned even when the reranker prunes:
// NaN means "keep the fused score", not "irrelevant".
func TestPruningRerankerKeepsUnscoredCandidates(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "a", Text: "alpha", Type: "world"},
		RecallMatch{ID: "b", Text: "alpha", Type: "world"},
	)
	pruned, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", RerankPrunesCandidates: true,
		Rerank: func(_ context.Context, _ string, texts []string) ([]float64, error) {
			scores := make([]float64, len(texts))
			for i := range scores {
				scores[i] = math.NaN()
			}
			scores[0] = 1.0
			return scores, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned.Matches) != 2 {
		t.Fatalf("an unscored candidate must survive pruning: %v", recallIDs(pruned))
	}
}
