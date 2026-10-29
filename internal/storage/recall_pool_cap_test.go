package storage

import (
	"context"
	"testing"
)

// The reranker candidate cap trims the fused pool before scoring: candidates
// past the cap are dropped rather than carried on their fused scores, and the
// cross-encoder never sees them (engine/memory_engine.py:10428-10431).
func TestRecallPoolTrimDropsPastCandidateCap(t *testing.T) {
	s := NewMemoryStore("public")
	tuneStore(t, s, func(tune *Tuning) { tune.RerankerMaxCandidates = 2 })
	seedBank(t, s, "b1",
		RecallMatch{ID: "c1", Text: "alpha", Type: "world"},
		RecallMatch{ID: "c2", Text: "alpha", Type: "world"},
		RecallMatch{ID: "c3", Text: "alpha", Type: "world"},
		RecallMatch{ID: "c4", Text: "alpha", Type: "world"},
	)
	scored := -1
	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		Rerank: func(_ context.Context, _ string, texts []string) ([]float64, error) {
			scored = len(texts)
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
	if scored != 2 {
		t.Fatalf("reranker scored %d candidates, want the capped pool of 2", scored)
	}
	if got := recallIDs(result); len(got) != 2 || got[0] != "c1" || got[1] != "c2" {
		t.Fatalf("pool trim must keep the best fused candidates: %v", got)
	}
}

// The cap resolves per budget level: a level's override wins when it is set,
// otherwise the flat cap answers (engine/memory_engine.py:9532 with :1715-1730).
func TestRecallPoolTrimResolvesPerBudget(t *testing.T) {
	s := NewMemoryStore("public")
	tuneStore(t, s, func(tune *Tuning) {
		tune.RerankerMaxCandidates = 3
		tune.RerankerMaxCandidatesLow = 1
	})
	seedBank(t, s, "b1",
		RecallMatch{ID: "c1", Text: "alpha", Type: "world"},
		RecallMatch{ID: "c2", Text: "alpha", Type: "world"},
		RecallMatch{ID: "c3", Text: "alpha", Type: "world"},
	)
	result, err := s.Recall(context.Background(), RecallQuery{BankID: "b1", Query: "alpha", Budget: BudgetLow})
	if err != nil {
		t.Fatal(err)
	}
	if got := recallIDs(result); len(got) != 1 || got[0] != "c1" {
		t.Fatalf("low budget must trim the pool to its own override: %v", got)
	}
}
