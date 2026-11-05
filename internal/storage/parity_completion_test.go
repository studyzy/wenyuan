package storage

import (
	"context"
	"math"
	"testing"
)

// A calibrated [0,1] batch keeps its absolute scores, including a real zero;
// any other batch is logits and is squashed through a sigmoid even when the
// magnitudes exceed the old min-max window.
func TestNormalizeRerankScoreKeepsCalibratedZeroAndSquashesLogits(t *testing.T) {
	calibrated := []float64{0, 0.2, 0.9}
	if got := normalizeRerankScore(0, calibrated); got != 0 {
		t.Fatalf("calibrated zero = %v, want 0", got)
	}
	if got := normalizeRerankScore(0.2, calibrated); got != 0.2 {
		t.Fatalf("calibrated score = %v, want 0.2", got)
	}
	logits := []float64{-100, 0, 100}
	if got, want := normalizeRerankScore(-100, logits), logistic(-100); math.Abs(got-want) > 1e-12 {
		t.Fatalf("large logits = %v, want sigmoid %v", got, want)
	}
}

// A passthrough (RRF) rerank has no cross-encoder score for the stage-2 add to
// correct, so the strategy bump is skipped there but still applies on a scored
// rerank.
func TestPassthroughRerankSkipsStrategyBoost(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{ID: "a", Text: "alpha", Type: "world"})
	rerank := func(_ context.Context, _ string, texts []string) ([]float64, error) {
		scores := make([]float64, len(texts))
		for i := range scores {
			scores[i] = 0.5
		}
		return scores, nil
	}
	boosts := map[string]string{ArmSemantic: "high"}

	plain, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Rerank: rerank, RerankSeedFromFused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	boosted, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Rerank: rerank, RerankSeedFromFused: true,
		StrategyBoosts: boosts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Matches) == 0 || len(boosted.Matches) == 0 {
		t.Fatal("no matches")
	}
	if plain.Matches[0].Final != boosted.Matches[0].Final {
		t.Fatalf("passthrough boost applied: %v vs %v", plain.Matches[0].Final, boosted.Matches[0].Final)
	}

	scoredPlain, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Rerank: rerank,
	})
	if err != nil {
		t.Fatal(err)
	}
	scoredBoosted, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha", Rerank: rerank, StrategyBoosts: boosts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if scoredBoosted.Matches[0].Final <= scoredPlain.Matches[0].Final {
		t.Fatalf("stage-2 boost missing on a scored rerank: %v vs %v", scoredBoosted.Matches[0].Final, scoredPlain.Matches[0].Final)
	}
}
