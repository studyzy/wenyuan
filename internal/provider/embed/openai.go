package embed

import (
	"context"
	"sort"
	"sync"

	"github.com/studyzy/wenyuan/internal/provider"
)

// bearerHeaders returns the Authorization header for a member that
// authenticates with a bearer token, or nil when it carries no credential.
func bearerHeaders(apiKey string) map[string]string {
	if apiKey == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + apiKey}
}

// openAIEmbedRequest is the request body the OpenAI-compatible embeddings
// endpoint accepts. dimensions is present only when the deployment asked for a
// truncated width; encoding_format is always "float" so the response is plain
// numbers.
type openAIEmbedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format"`
}

// openAIEmbedResponse is the response body the OpenAI-compatible endpoint
// returns: one embedding per input, tagged with its input index.
type openAIEmbedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// openAICompat serves the OpenAI-compatible members (openai, openrouter,
// requesty, litellm). They differ only in endpoint, default credential, and
// whether a credential is required at all, so one adapter serves them.
type openAICompat struct {
	name          string
	http          *httpBackend
	apiKey        string
	model         string
	dimensions    int
	vendorDims    int
	batchSize     int
	maxConcurrent int
	passagePrefix string
	queryPrefix   string
}

// Name is the configured provider name.
func (b *openAICompat) Name() string { return b.name }

// Model is the configured model identifier.
func (b *openAICompat) Model() string { return b.model }

// Dimensions is the width this backend guarantees for every vector.
func (b *openAICompat) Dimensions() int { return b.dimensions }

// Embed generates one vector per input text, in input order, applying the
// configured prefix for the request kind and batching with bounded
// concurrency.
func (b *openAICompat) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	texts := applyPrefix(req.Texts, req.Kind, b.passagePrefix, b.queryPrefix)
	if len(texts) == 0 {
		return provider.EmbeddingResult{Dimensions: b.dimensions}, nil
	}
	// Every batch call reports its own billing, so the result carries the sum
	// across the batches this request fanned out into.
	var usage provider.Usage
	var usageMu sync.Mutex
	fetch := func(ctx context.Context, texts []string) ([][]float32, error) {
		vectors, callUsage, err := b.fetch(ctx, texts)
		usageMu.Lock()
		usage.InputTokens += callUsage.InputTokens
		usage.TotalTokens += callUsage.TotalTokens
		usageMu.Unlock()
		return vectors, err
	}
	vectors, err := batchEmbed(ctx, texts, b.batchSize, b.maxConcurrent, fetch)
	if err != nil {
		return provider.EmbeddingResult{}, err
	}
	return provider.EmbeddingResult{Vectors: vectors, Dimensions: b.dimensions, Usage: usage}, nil
}

// fetch issues one batch request and returns its vectors in input order,
// re-sorting by the index the endpoint reported, plus the call's billing.
func (b *openAICompat) fetch(ctx context.Context, texts []string) ([][]float32, provider.Usage, error) {
	raw, err := b.http.postJSON(ctx, "/embeddings", openAIEmbedRequest{
		Model:          b.model,
		Input:          texts,
		Dimensions:     b.vendorDims,
		EncodingFormat: "float",
	}, bearerHeaders(b.apiKey))
	if err != nil {
		return nil, provider.Usage{}, err
	}
	var wire openAIEmbedResponse
	if err := decodeJSON(raw, &wire, "cannot decode embedding response"); err != nil {
		return nil, provider.Usage{}, err
	}
	sort.Slice(wire.Data, func(i, j int) bool { return wire.Data[i].Index < wire.Data[j].Index })
	vectors := make([][]float32, 0, len(wire.Data))
	for _, item := range wire.Data {
		vectors = append(vectors, item.Embedding)
	}
	if err := onePerInput(vectors, len(texts), b.name); err != nil {
		return nil, provider.Usage{}, err
	}
	return vectors, provider.Usage{InputTokens: wire.Usage.PromptTokens, TotalTokens: wire.Usage.TotalTokens}, nil
}

// newOpenAICompat builds an OpenAI-compatible member from the resolved
// configuration.
func newOpenAICompat(cfg Config, name, baseURL, apiKeyKey string) (Embedder, error) {
	if apiKeyKey != "" && cfg.APIKey == "" {
		return nil, &provider.MissingCredentialError{Provider: name, ConfigKey: apiKeyKey}
	}
	if baseURL == "" {
		baseURL = DefaultOpenAIBaseURL
	}
	return &openAICompat{
		name:          name,
		http:          newHTTPBackend(name, baseURL, cfg.Timeout, cfg.Retry),
		apiKey:        cfg.APIKey,
		model:         cfg.Model,
		dimensions:    resolveDimensions(cfg),
		vendorDims:    cfg.Dimensions,
		batchSize:     resolveBatch(cfg, DefaultBatchSize),
		maxConcurrent: resolveMaxConcurrent(cfg),
		passagePrefix: cfg.PassagePrefix,
		queryPrefix:   cfg.QueryPrefix,
	}, nil
}
