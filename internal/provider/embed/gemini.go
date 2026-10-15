package embed

import (
	"context"
	"net/url"

	"github.com/studyzy/wenyuan/internal/provider"
)

// geminiEmbedItem is one text in a Gemini batch embed request. Each text is
// its own content so every input comes back as its own vector rather than
// being fused into one.
type geminiEmbedItem struct {
	Model                string        `json:"model"`
	Content              geminiContent `json:"content"`
	OutputDimensionality int           `json:"outputDimensionality,omitempty"`
}

// geminiContent is a single content part carrying one input text.
type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

// geminiPart is a text part.
type geminiPart struct {
	Text string `json:"text"`
}

// geminiEmbedRequest is the request body batchEmbedContents accepts.
type geminiEmbedRequest struct {
	Requests []geminiEmbedItem `json:"requests"`
}

// geminiEmbedResponse is the response body batchEmbedContents returns.
type geminiEmbedResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
}

// geminiBackend serves the Gemini API-key member. The key travels as a query
// parameter, not as a header, and the model name is carried both in the URL
// path and in each request item.
type geminiBackend struct {
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
func (b *geminiBackend) Name() string { return "gemini" }

// Dimensions is the width this backend guarantees for every vector.
func (b *geminiBackend) Dimensions() int { return b.dimensions }

// Embed generates one vector per input text, in input order, applying the
// configured prefix for the request kind.
func (b *geminiBackend) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	texts := applyPrefix(req.Texts, req.Kind, b.passagePrefix, b.queryPrefix)
	if len(texts) == 0 {
		return provider.EmbeddingResult{Dimensions: b.dimensions}, nil
	}
	vectors, err := batchEmbed(ctx, texts, b.batchSize, b.maxConcurrent, b.fetch)
	if err != nil {
		return provider.EmbeddingResult{}, err
	}
	return provider.EmbeddingResult{Vectors: vectors, Dimensions: b.dimensions}, nil
}

// fetch issues one batch request and returns its vectors in input order.
func (b *geminiBackend) fetch(ctx context.Context, texts []string) ([][]float32, error) {
	items := make([]geminiEmbedItem, 0, len(texts))
	for _, text := range texts {
		items = append(items, geminiEmbedItem{
			Model: "models/" + b.model,
			Content: geminiContent{
				Parts: []geminiPart{{Text: text}},
			},
			OutputDimensionality: b.vendorDims,
		})
	}
	path := "/v1beta/models/" + b.model + ":batchEmbedContents?key=" + url.QueryEscape(b.apiKey)
	raw, err := b.http.postJSON(ctx, path, geminiEmbedRequest{Requests: items}, nil)
	if err != nil {
		return nil, err
	}
	var wire geminiEmbedResponse
	if err := decodeJSON(raw, &wire, "cannot decode embedding response"); err != nil {
		return nil, err
	}
	vectors := make([][]float32, 0, len(wire.Embeddings))
	for _, item := range wire.Embeddings {
		vectors = append(vectors, item.Values)
	}
	if err := onePerInput(vectors, len(texts), "gemini"); err != nil {
		return nil, err
	}
	return vectors, nil
}

// newGemini builds the Gemini API-key member from the resolved
// configuration.
func newGemini(cfg Config) (Embedder, error) {
	if cfg.APIKey == "" {
		return nil, &provider.MissingCredentialError{Provider: "gemini", ConfigKey: keyGeminiAPIKey}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultGeminiBaseURL
	}
	model := cfg.Model
	if model == "" {
		model = DefaultGeminiModel
	}
	return &geminiBackend{
		http:          newHTTPBackend("gemini", baseURL, cfg.Timeout, cfg.Retry),
		apiKey:        cfg.APIKey,
		model:         model,
		dimensions:    resolveDimensions(cfg),
		vendorDims:    cfg.Dimensions,
		batchSize:     resolveBatch(cfg, DefaultBatchSize),
		maxConcurrent: resolveMaxConcurrent(cfg),
		passagePrefix: cfg.PassagePrefix,
		queryPrefix:   cfg.QueryPrefix,
	}, nil
}
