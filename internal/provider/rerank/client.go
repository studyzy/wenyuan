package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// bankIDHeader carries the calling bank to a reranker whose deployment opted
// into per-bank attribution.
const bankIDHeader = "X-Hindsight-Bank-Id"

// tokenEstimate approximates the token count of a text as its whitespace word
// count. The reference counts tokens with the backend's own tokenizer; this
// build deliberately uses the whitespace estimate instead, so the package does
// not carry a per-vendor tokenizer. It is only ever used to decide how much
// text fits a budget, where being off by a small factor is harmless.
func tokenEstimate(text string) int { return len(strings.Fields(text)) }

// truncateTokens keeps the first maxTokens whitespace words of text.
func truncateTokens(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) <= maxTokens {
		return text
	}
	return strings.Join(fields[:maxTokens], " ")
}

// poster is the one transport every network-backed member shares, so request
// framing, retry and error classification are defined once instead of per
// vendor. Transport faults and 5xx/429 responses are transient and retried
// under the rerank retry policy; every other failure is permanent.
type poster struct {
	client   *http.Client
	policy   provider.RetryPolicy
	sendBank bool
	// slots bounds this member's simultaneous in-flight requests.
	slots chan struct{}
}

func newPoster(timeout time.Duration, policy provider.RetryPolicy, sendBank bool, maxConcurrent int) *poster {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	return &poster{
		client:   &http.Client{Timeout: timeout},
		policy:   policy,
		sendBank: sendBank,
		slots:    make(chan struct{}, maxConcurrent),
	}
}

// headers builds the request headers: the vendor credential plus, when the
// deployment forwards it, the calling bank taken from the context.
func (p *poster) headers(ctx context.Context, apiKey string) map[string]string {
	headers := map[string]string{}
	if apiKey != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}
	if p.sendBank {
		if tc, ok := provider.TraceContextFrom(ctx); ok && tc.BankID != "" {
			headers[bankIDHeader] = tc.BankID
		}
	}
	return headers
}

// post sends payload as JSON and returns the raw response body, retrying
// transient failures within the policy's attempt budget.
func (p *poster) post(ctx context.Context, url string, headers map[string]string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot encode rerank request", Cause: err}
	}
	attempts := p.policy.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(p.policy.BackoffFor(attempt - 1)):
			}
		}
		raw, err := p.attempt(ctx, url, headers, body)
		if err == nil {
			return raw, nil
		}
		last = err
		if !provider.IsTransient(err) {
			return nil, err
		}
	}
	return nil, last
}

// attempt runs one request under this member's concurrency bound, so a burst of
// recall calls cannot open more sockets to one backend than it was configured
// for.
func (p *poster) attempt(ctx context.Context, url string, headers map[string]string, body []byte) ([]byte, error) {
	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.slots }()
	return p.once(ctx, url, headers, body)
}

func (p *poster) once(ctx context.Context, url string, headers map[string]string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &provider.TransientError{Cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &provider.TransientError{Cause: err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	return nil, statusError(resp.StatusCode, raw)
}

// statusError classifies a non-2xx response: overload and server faults are
// transient, anything else is the endpoint rejecting this request.
func statusError(status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if len(detail) > 300 {
		detail = detail[:300]
	}
	cause := fmt.Errorf("rerank endpoint returned HTTP %d: %s", status, detail)
	if status == http.StatusTooManyRequests || status >= 500 {
		return &provider.TransientError{Cause: cause}
	}
	return cause
}

// cohereRequest is the request body the Cohere-shaped endpoints accept. It is
// the shape SiliconFlow, ZeroEntropy, OpenRouter and a Cohere-compatible host
// all speak.
type cohereRequest struct {
	Model           string   `json:"model"`
	Query           string   `json:"query"`
	Documents       []string `json:"documents"`
	TopN            *int     `json:"top_n,omitempty"`
	ReturnDocuments bool     `json:"return_documents"`
}

// cohereResponse is the response body those endpoints return.
type cohereResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

// cohereClient adapts a Cohere-shaped /rerank endpoint to the reranker
// boundary. includeTopN is false for hosts whose URL already carries the full
// invoke path and whose contract rejects the field.
type cohereClient struct {
	poster      *poster
	url         string
	apiKey      string
	model       string
	includeTopN bool
}

func (c *cohereClient) Rerank(ctx context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	if len(req.Documents) == 0 {
		return provider.RerankResult{}, nil
	}
	model := req.Model
	if model == "" {
		model = c.model
	}
	body := cohereRequest{
		Model:           model,
		Query:           req.Query,
		Documents:       req.Documents,
		ReturnDocuments: false,
	}
	if c.includeTopN {
		count := len(req.Documents)
		body.TopN = &count
	}
	raw, err := c.poster.post(ctx, c.url, c.poster.headers(ctx, c.apiKey), body)
	if err != nil {
		return provider.RerankResult{}, err
	}
	return decodeCohere(raw, len(req.Documents))
}

// decodeCohere places each reported score at the document index the backend
// named, so the result is one score per input document in input order.
func decodeCohere(raw []byte, expected int) (provider.RerankResult, error) {
	var wire cohereResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return provider.RerankResult{}, &provider.MalformedResponseError{Detail: "cannot decode rerank response", Cause: err}
	}
	if len(wire.Results) == 0 {
		return provider.RerankResult{}, &provider.MalformedResponseError{Detail: "rerank response carried no results"}
	}
	scores := make([]float64, expected)
	for _, item := range wire.Results {
		if item.Index < 0 || item.Index >= expected {
			return provider.RerankResult{}, &provider.MalformedResponseError{
				Detail: fmt.Sprintf("rerank result index %d is out of range for %d documents", item.Index, expected),
			}
		}
		scores[item.Index] = item.RelevanceScore
	}
	return provider.RerankResult{Scores: scores}, nil
}

// teiRequest is the HuggingFace text-embeddings-inference rerank body.
type teiRequest struct {
	Query string   `json:"query"`
	Texts []string `json:"texts"`
}

// teiResult is one entry of the TEI rerank response.
type teiResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// teiClient adapts a self-hosted text-embeddings-inference reranker.
type teiClient struct {
	poster *poster
	url    string
}

func (c *teiClient) Rerank(ctx context.Context, req provider.RerankRequest) (provider.RerankResult, error) {
	if len(req.Documents) == 0 {
		return provider.RerankResult{}, nil
	}
	raw, err := c.poster.post(ctx, c.url, c.poster.headers(ctx, ""), teiRequest{
		Query: req.Query,
		Texts: req.Documents,
	})
	if err != nil {
		return provider.RerankResult{}, err
	}
	var wire []teiResult
	if err := json.Unmarshal(raw, &wire); err != nil {
		return provider.RerankResult{}, &provider.MalformedResponseError{Detail: "cannot decode TEI rerank response", Cause: err}
	}
	if len(wire) == 0 {
		return provider.RerankResult{}, &provider.MalformedResponseError{Detail: "TEI rerank response carried no results"}
	}
	scores := make([]float64, len(req.Documents))
	for _, item := range wire {
		if item.Index < 0 || item.Index >= len(scores) {
			return provider.RerankResult{}, &provider.MalformedResponseError{
				Detail: fmt.Sprintf("TEI rerank index %d is out of range for %d documents", item.Index, len(scores)),
			}
		}
		scores[item.Index] = item.Score
	}
	return provider.RerankResult{Scores: scores}, nil
}
