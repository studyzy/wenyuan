package openai

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// test-rerank-one-score-per-document-in-order: a rerank call returns exactly
// one score per document, in input order, re-sorted by the index the backend
// reported rather than by response order.
func TestRerankScoresInDocumentOrder(t *testing.T) {
	body := `{"results":[` +
		`{"index":2,"relevance_score":0.3},` +
		`{"index":0,"relevance_score":0.9},` +
		`{"index":1,"relevance_score":0.1}` +
		`]}`
	c, _ := testClient(jsonResponse(body))
	result, err := c.Rerank(context.Background(), provider.RerankRequest{
		Query:     "q",
		Documents: []string{"a", "b", "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{0.9, 0.1, 0.3}
	if len(result.Scores) != len(want) {
		t.Fatalf("scores = %v, want %v", result.Scores, want)
	}
	for i, value := range want {
		if result.Scores[i] != value {
			t.Fatalf("scores = %v, want %v", result.Scores, want)
		}
	}
}

// test-rerank-short-response-rejected: a response that does not carry one score
// per document is malformed, not silently paired with the wrong document.
func TestRerankShortResponseRejected(t *testing.T) {
	c, _ := testClient(jsonResponse(`{"results":[{"index":0,"relevance_score":0.5}]}`))
	if _, err := c.Rerank(context.Background(), provider.RerankRequest{
		Query:     "q",
		Documents: []string{"a", "b"},
	}); err == nil {
		t.Fatal("a response with fewer scores than documents must be rejected")
	}
}

// An empty document list is answered without a provider call.
func TestRerankEmptyDocuments(t *testing.T) {
	c, doer := testClient()
	result, err := c.Rerank(context.Background(), provider.RerankRequest{Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Scores) != 0 {
		t.Fatalf("scores = %v, want none", result.Scores)
	}
	if doer.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", doer.calls)
	}
}

// The rerank model falls back to the completion model so a gateway serving both
// from one name needs no extra configuration.
func TestRerankModelFallsBack(t *testing.T) {
	c, _ := testClient()
	c.Settings.Model = "shared-model"
	if got := c.rerankModel(); got != "shared-model" {
		t.Fatalf("rerank model = %q, want the completion model", got)
	}
	c.Settings.Rerank = &provider.RerankSettings{Model: "rerank-model"}
	if got := c.rerankModel(); got != "rerank-model" {
		t.Fatalf("rerank model = %q, want the configured rerank model", got)
	}
}
