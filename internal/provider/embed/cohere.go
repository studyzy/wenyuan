package embed

import (
	"context"

	"github.com/studyzy/wenyuan/internal/provider"
)

// cohereEmbedRequest is the request body the Cohere embeddings endpoint
// accepts. The retrieval task travels as input_type rather than as a text
// prefix, so this member never prepends one.
type cohereEmbedRequest struct {
	Texts          []string `json:"texts"`
	Model          string   `json:"model"`
	InputType      string   `json:"input_type"`
	EmbeddingTypes []string `json:"embedding_types,omitempty"`
}

// cohereEmbedResponse is the response body the Cohere embeddings endpoint
// returns, with the float vectors under a typed envelope.
type cohereEmbedResponse struct {
	Embeddings struct {
		Float [][]float32 `json:"float"`
	} `json:"embeddings"`
}

// cohereBackend serves the Cohere member. It selects the retrieval task from
// the request kind instead of a text prefix.
type cohereBackend struct {
	http          *httpBackend
	apiKey        string
	model         string
	dimensions    int
	batchSize     int
	maxConcurrent int
}

// Name is the configured provider name.
func (b *cohereBackend) Name() string { return "cohere" }

// Model is the effective model identifier, the adapter default applied.
func (b *cohereBackend) Model() string { return b.model }

// Dimensions is the width this backend guarantees for every vector.
func (b *cohereBackend) Dimensions() int { return b.dimensions }

// Embed generates one vector per input text, in input order. The kind picks
// Cohere's input_type rather than a prefix: search_query for queries,
// search_document for passages.
func (b *cohereBackend) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	if len(req.Texts) == 0 {
		return provider.EmbeddingResult{Dimensions: b.dimensions}, nil
	}
	inputType := "search_document"
	if req.Kind == provider.EmbeddingQuery {
		inputType = "search_query"
	}
	vectors, err := batchEmbed(ctx, req.Texts, b.batchSize, b.maxConcurrent, func(ctx context.Context, texts []string) ([][]float32, error) {
		return b.fetch(ctx, texts, inputType)
	})
	if err != nil {
		return provider.EmbeddingResult{}, err
	}
	return provider.EmbeddingResult{Vectors: vectors, Dimensions: b.dimensions}, nil
}

// fetch issues one batch request and returns its float vectors in input order.
func (b *cohereBackend) fetch(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	raw, err := b.http.postJSON(ctx, "/embed", cohereEmbedRequest{
		Texts:          texts,
		Model:          b.model,
		InputType:      inputType,
		EmbeddingTypes: []string{"float"},
	}, bearerHeaders(b.apiKey))
	if err != nil {
		return nil, err
	}
	var wire cohereEmbedResponse
	if err := decodeJSON(raw, &wire, "cannot decode embedding response"); err != nil {
		return nil, err
	}
	if err := onePerInput(wire.Embeddings.Float, len(texts), "cohere"); err != nil {
		return nil, err
	}
	return wire.Embeddings.Float, nil
}

// newCohere builds the Cohere member from the resolved configuration.
func newCohere(cfg Config) (Embedder, error) {
	if cfg.APIKey == "" {
		return nil, &provider.MissingCredentialError{Provider: "cohere", ConfigKey: keyCohereAPIKey}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultCohereBaseURL
	}
	model := cfg.Model
	if model == "" {
		model = DefaultCohereModel
	}
	return &cohereBackend{
		http:          newHTTPBackend("cohere", baseURL, cfg.Timeout, cfg.Retry),
		apiKey:        cfg.APIKey,
		model:         model,
		dimensions:    resolveDimensions(cfg),
		batchSize:     resolveBatch(cfg, DefaultBatchSize),
		maxConcurrent: resolveMaxConcurrent(cfg),
	}, nil
}
