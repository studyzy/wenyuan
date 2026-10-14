package app

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/none"
)

// rerankingStub is a provider that also implements the reranker boundary.
type rerankingStub struct {
	none.Provider
	scores []float64
	calls  int
}

func (r *rerankingStub) Name() string { return "stub" }

// Rerank records the call and returns the scripted scores.
func (r *rerankingStub) Rerank(_ context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	r.calls++
	return provider.RerankResult{Scores: r.scores[:len(req.Documents)]}, nil
}

// plainStub is a provider that cannot rerank, so the recall pipeline must keep
// its best-per-arm fallback.
type plainStub struct {
	none.Provider
}

func (p *plainStub) Name() string { return "plain" }

// A provider with no reranking capability yields no recall reranker, so a
// deployment whose model cannot score pairs keeps working on the fallback.
func TestNoRerankerWithoutCapability(t *testing.T) {
	if got := newRecallReranker(&plainStub{}); got != nil {
		t.Fatalf("a provider without a reranker must yield nil, got %T", got)
	}
}

// The no-model selection yields no reranker, matching the other model-backed
// boundaries.
func TestNoRerankerForNoModelSelection(t *testing.T) {
	if got := newRecallReranker(none.New()); got != nil {
		t.Fatalf("the no-model selection must yield nil, got %T", got)
	}
	if got := newRecallReranker(nil); got != nil {
		t.Fatalf("a nil provider must yield nil, got %T", got)
	}
}

// A provider that can rerank is adapted onto the recall boundary and returns
// one score per candidate in order.
func TestRerankerAdapterScoresInOrder(t *testing.T) {
	stub := &rerankingStub{scores: []float64{0.9, 0.1}}
	reranker := newRecallReranker(stub)
	if reranker == nil {
		t.Fatal("a reranking provider must yield a reranker")
	}
	scores, err := reranker.Rerank(context.Background(), "q", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scores) != 2 || scores[0] != 0.9 || scores[1] != 0.1 {
		t.Fatalf("scores = %v, want [0.9 0.1]", scores)
	}
	if stub.calls != 1 {
		t.Fatalf("reranker calls = %d, want 1", stub.calls)
	}
}
