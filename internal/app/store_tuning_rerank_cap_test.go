package app

import (
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// tuningSpy captures the tuning the composition root installs, so a test can
// assert a knob reached the store without poking at the store's internals.
type tuningSpy struct {
	*storage.MemoryStore
	got storage.Tuning
}

func (s *tuningSpy) SetTuning(tuning storage.Tuning) { s.got = tuning }

// The reranker candidate cap rides the store tuning like the budget caps do, so
// the recall pipeline can resolve the per-budget-level value per recall.
func TestSetStoreTuningCarriesRerankerCandidateCap(t *testing.T) {
	spy := &tuningSpy{MemoryStore: storage.NewMemoryStore("public")}
	cfg := config.Config{}
	cfg.Provider.Rerank.CandidateDepth = 300
	cfg.Provider.Rerank.CandidateDepthLow = 100
	cfg.Provider.Rerank.CandidateDepthMid = 200
	cfg.Provider.Rerank.CandidateDepthHigh = 400
	setStoreTuning(spy, cfg)
	if spy.got.RerankerMaxCandidates != 300 ||
		spy.got.RerankerMaxCandidatesLow != 100 ||
		spy.got.RerankerMaxCandidatesMid != 200 ||
		spy.got.RerankerMaxCandidatesHigh != 400 {
		t.Fatalf("reranker candidate cap = %+v", spy.got)
	}
}
