package app

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
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

// A provider with no reranking capability, and no dedicated reranker, yields no
// recall reranker, so a deployment whose model cannot score pairs keeps working
// on the fallback.
func TestNoRerankerWithoutCapability(t *testing.T) {
	if got, _, _, _, _ := recallRerankerWiring(config.Config{}, &plainStub{}, nil); got != nil {
		t.Fatalf("a provider without a reranker must yield nil, got %T", got)
	}
}

// The no-model selection yields no reranker, matching the other model-backed
// boundaries.
func TestNoRerankerForNoModelSelection(t *testing.T) {
	if got, _, _, _, _ := recallRerankerWiring(config.Config{}, none.New(), nil); got != nil {
		t.Fatalf("the no-model selection must yield nil, got %T", got)
	}
	if got, _, _, _, _ := recallRerankerWiring(config.Config{}, nil, nil); got != nil {
		t.Fatalf("a nil provider must yield nil, got %T", got)
	}
}

// A provider that can rerank is adapted onto the recall boundary and returns
// one score per candidate in order.
func TestRerankerAdapterScoresInOrder(t *testing.T) {
	stub := &rerankingStub{scores: []float64{0.9, 0.1}}
	reranker, _, byRank, prunes, seed := recallRerankerWiring(config.Config{}, stub, nil)
	if reranker == nil {
		t.Fatal("a reranking provider must yield a reranker")
	}
	if byRank || prunes || seed {
		t.Fatalf("a plain chat reranker must set no flags, got byRank=%v prunes=%v seed=%v", byRank, prunes, seed)
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

// A dedicated RERANKER-family backend wins over the chat provider's own
// capability, and the candidate depth is carried through from the resolved
// configuration.
func TestDedicatedRerankerWinsAndCarriesDepth(t *testing.T) {
	chat := &rerankingStub{scores: []float64{0.1, 0.9}}
	dedicated := &rerankingStub{scores: []float64{0.4, 0.6}}
	cfg := config.Config{}
	cfg.Provider.Rerank.CandidateDepth = 25
	reranker, depth, _, _, _ := recallRerankerWiring(cfg, chat, dedicated)
	if reranker == nil {
		t.Fatal("a dedicated reranker must yield a reranker")
	}
	if depth != 25 {
		t.Fatalf("depth = %d, want 25", depth)
	}
	scores, err := reranker.Rerank(context.Background(), "q", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if dedicated.calls != 1 || chat.calls != 0 {
		t.Fatalf("dedicated calls = %d, chat calls = %d, want 1 and 0", dedicated.calls, chat.calls)
	}
	if scores[0] != 0.4 {
		t.Fatalf("scores = %v, want the dedicated reranker's", scores)
	}
}
