package openai

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/studyzy/wenyuan/internal/provider"
)

// DefaultBatchSize is how many inputs go in one provider request when the
// embedding settings configure none.
const DefaultBatchSize = 64

// DefaultMaxConcurrentRequests bounds in-flight embedding requests per encode
// call when the settings configure none.
const DefaultMaxConcurrentRequests = 8

// embeddingRequest is the request body the embeddings endpoint accepts.
type embeddingRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format"`
}

// embeddingResponse is the response body the embeddings endpoint returns.
type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage usageWire `json:"usage"`
}

// Embed generates one vector per input text, in input order, at the
// boundary's declared dimension. Inputs are split into provider-sized batches
// issued with bounded concurrency; responses are reassembled by input index,
// never by completion order.
func (c *Client) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	if len(req.Texts) == 0 {
		return provider.EmbeddingResult{Dimensions: provider.DeclaredDimensions()}, nil
	}
	prefixed := c.applyPrefix(req.Texts, req.Kind)
	batches := batchOf(prefixed, c.batchSize())

	out := make([][]float32, len(prefixed))
	limit := c.maxConcurrentRequests()
	permits := newSemaphore(limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for start := 0; start < len(prefixed); start += batches {
		end := start + batches
		if end > len(prefixed) {
			end = len(prefixed)
		}
		chunk := prefixed[start:end]
		offset := start
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := permits.acquire()
			defer release()
			vectors, err := c.embedBatch(ctx, chunk, req.Retry)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			for i, v := range vectors {
				out[offset+i] = v
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return provider.EmbeddingResult{}, firstErr
	}
	return provider.EmbeddingResult{Vectors: out, Dimensions: provider.DeclaredDimensions()}, nil
}

// embedBatch issues one provider request and returns its vectors in input
// order, re-sorting by the provider-reported index.
func (c *Client) embedBatch(ctx context.Context, texts []string, retry *provider.RetryPolicy) ([][]float32, error) {
	body, err := json.Marshal(embeddingRequest{
		Model:          c.EmbeddingModel(),
		Input:          texts,
		Dimensions:     c.embeddingVendorDimensions(),
		EncodingFormat: "float",
	})
	if err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot encode embedding request", Cause: err}
	}
	policy := retryPolicyFor(retry, c.embeddingRetryPolicy())
	raw, err := c.sendWithRetry(ctx, "embeddings", body, policy, provider.ScopeChat, nil)
	if err != nil {
		return nil, err
	}
	return decodeEmbeddings(raw, len(texts))
}

// decodeEmbeddings turns a success body into vectors ordered by input index.
func decodeEmbeddings(raw []byte, expected int) ([][]float32, error) {
	var wire embeddingResponse
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot decode embedding response", Cause: err}
	}
	if len(wire.Data) != expected {
		return nil, &provider.MalformedResponseError{Detail: "embedding response did not return one vector per input"}
	}
	sort.Slice(wire.Data, func(i, j int) bool { return wire.Data[i].Index < wire.Data[j].Index })
	out := make([][]float32, 0, len(wire.Data))
	for _, item := range wire.Data {
		out = append(out, item.Embedding)
	}
	return out, nil
}

// applyPrefix prepends the configured passage or query prefix to each input.
func (c *Client) applyPrefix(texts []string, kind provider.EmbeddingKind) []string {
	prefix := c.passagePrefix()
	if kind == provider.EmbeddingQuery {
		prefix = c.queryPrefix()
	}
	if prefix == "" {
		return texts
	}
	out := make([]string, 0, len(texts))
	for _, t := range texts {
		out = append(out, prefix+t)
	}
	return out
}

// batchSize resolves the provider-sized batch from the settings or default.
func (c *Client) batchSize() int {
	if c.Settings.Embedding != nil && c.Settings.Embedding.BatchSize > 0 {
		return c.Settings.Embedding.BatchSize
	}
	return DefaultBatchSize
}

// maxConcurrentRequests resolves the in-flight bound from the settings or
// default.
func (c *Client) maxConcurrentRequests() int {
	if c.Settings.Embedding != nil && c.Settings.Embedding.MaxConcurrentRequests > 0 {
		return c.Settings.Embedding.MaxConcurrentRequests
	}
	return DefaultMaxConcurrentRequests
}

// batchOf reports the batch size, never less than one.
func batchOf(texts []string, size int) int {
	if size <= 0 {
		size = DefaultBatchSize
	}
	if size > len(texts) && len(texts) > 0 {
		return len(texts)
	}
	return size
}
