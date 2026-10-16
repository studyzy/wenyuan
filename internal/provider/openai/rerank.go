package openai

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/studyzy/wenyuan/internal/provider"
)

// rerankRequest is the request body the rerank endpoint accepts. It is the
// widely supported shape (Jina, Cohere, and OpenAI-compatible gateways), where
// documents are plain strings and the response carries an index per document.
type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

// rerankResponse is the response body the rerank endpoint returns.
type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// rerankModel resolves the model identifier, falling back to the client's
// completion model so a gateway that serves both from one name works without
// extra configuration.
func (c *Client) rerankModel() string {
	if c.Settings.Rerank != nil && c.Settings.Rerank.Model != "" {
		return c.Settings.Rerank.Model
	}
	return c.Settings.Model
}

// Rerank scores each document against the query, in input order.
//
// The scores are the backend's own relevance scale, not normalized here: the
// recall pipeline normalizes them against the batch it received, which is what
// makes a score comparable to the per-arm scores it replaces.
func (c *Client) Rerank(ctx context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	if len(req.Documents) == 0 {
		return provider.RerankResult{}, nil
	}
	body, err := json.Marshal(rerankRequest{
		Model:     c.rerankModel(),
		Query:     req.Query,
		Documents: req.Documents,
	})
	if err != nil {
		return provider.RerankResult{}, &provider.MalformedResponseError{Detail: "cannot encode rerank request", Cause: err}
	}
	policy := c.rerankRetryPolicy()
	raw, err := c.sendWithRetry(ctx, "rerank", body, policy, provider.ScopeChat, nil)
	if err != nil {
		return provider.RerankResult{}, err
	}
	return decodeRerank(raw, len(req.Documents))
}

// decodeRerank turns a success body into one score per input document, ordered
// by the document index the backend reported.
func decodeRerank(raw []byte, expected int) (provider.RerankResult, error) {
	var wire rerankResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return provider.RerankResult{}, &provider.MalformedResponseError{Detail: "cannot decode rerank response", Cause: err}
	}
	if len(wire.Results) != expected {
		return provider.RerankResult{}, &provider.MalformedResponseError{Detail: "rerank response did not return one score per document"}
	}
	sort.Slice(wire.Results, func(i, j int) bool { return wire.Results[i].Index < wire.Results[j].Index })
	scores := make([]float64, 0, len(wire.Results))
	for _, item := range wire.Results {
		scores = append(scores, item.RelevanceScore)
	}
	return provider.RerankResult{Scores: scores}, nil
}

// rerankRetryPolicy resolves the rerank retry tuning, falling back to the
// client's completion policy so a deployment that configured only the main
// scope still gets a sane retry budget.
func (c *Client) rerankRetryPolicy() provider.RetryPolicy {
	if c.Settings.Rerank != nil {
		return retryPolicyFor(nil, c.Settings.Rerank.Retry)
	}
	return retryPolicyFor(nil, c.Settings.Retry)
}
