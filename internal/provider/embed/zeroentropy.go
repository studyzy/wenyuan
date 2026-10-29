package embed

import (
	"context"

	"github.com/studyzy/wenyuan/internal/provider"
)

// zeroEntropyEmbedRequest is the request body ZeroEntropy's non-OpenAI
// compatible embed endpoint accepts. The retrieval side travels as input_type,
// so this member never prepends a text prefix.
type zeroEntropyEmbedRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	InputType      string   `json:"input_type"`
	Dimensions     int      `json:"dimensions,omitempty"`
	EncodingFormat string   `json:"encoding_format"`
}

// zeroEntropyEmbedResponse is the response body the embed endpoint returns.
// The float vectors arrive either as a Cohere-shaped envelope or as a bare
// results array, so both are accepted.
type zeroEntropyEmbedResponse struct {
	Embeddings struct {
		Float [][]float32 `json:"float"`
	} `json:"embeddings"`
	Results []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"results"`
}

// zeroEntropyBackend serves the ZeroEntropy member. It selects the retrieval
// side from the request kind instead of a text prefix.
type zeroEntropyBackend struct {
	http          *httpBackend
	apiKey        string
	model         string
	dimensions    int
	vendorDims    int
	batchSize     int
	maxConcurrent int
}

// Name is the configured provider name.
func (b *zeroEntropyBackend) Name() string { return "zeroentropy" }

// Model is the effective model identifier, the adapter default applied.
func (b *zeroEntropyBackend) Model() string { return b.model }

// Dimensions is the width this backend guarantees for every vector.
func (b *zeroEntropyBackend) Dimensions() int { return b.dimensions }

// Embed generates one vector per input text, in input order. The kind picks
// the embed endpoint's input_type rather than a prefix: query for queries,
// document for passages.
func (b *zeroEntropyBackend) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	if len(req.Texts) == 0 {
		return provider.EmbeddingResult{Dimensions: b.dimensions}, nil
	}
	inputType := "document"
	if req.Kind == provider.EmbeddingQuery {
		inputType = "query"
	}
	vectors, err := batchEmbed(ctx, req.Texts, b.batchSize, b.maxConcurrent, func(ctx context.Context, texts []string) ([][]float32, error) {
		return b.fetch(ctx, texts, inputType)
	})
	if err != nil {
		return provider.EmbeddingResult{}, err
	}
	return provider.EmbeddingResult{Vectors: vectors, Dimensions: b.dimensions}, nil
}

// fetch issues one batch request and returns its vectors in input order.
func (b *zeroEntropyBackend) fetch(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	raw, err := b.http.postJSON(ctx, "/v1/models/embed", zeroEntropyEmbedRequest{
		Model:          b.model,
		Input:          texts,
		InputType:      inputType,
		Dimensions:     b.vendorDims,
		EncodingFormat: "float",
	}, bearerHeaders(b.apiKey))
	if err != nil {
		return nil, err
	}
	var wire zeroEntropyEmbedResponse
	if err := decodeJSON(raw, &wire, "cannot decode embedding response"); err != nil {
		return nil, err
	}
	vectors := wire.Embeddings.Float
	if len(vectors) == 0 && len(wire.Results) > 0 {
		vectors = make([][]float32, 0, len(wire.Results))
		for _, item := range wire.Results {
			vectors = append(vectors, item.Embedding)
		}
	}
	if err := onePerInput(vectors, len(texts), "zeroentropy"); err != nil {
		return nil, err
	}
	return vectors, nil
}

// newZeroEntropy builds the ZeroEntropy member from the resolved
// configuration.
func newZeroEntropy(cfg Config) (Embedder, error) {
	if cfg.APIKey == "" {
		return nil, &provider.MissingCredentialError{Provider: "zeroentropy", ConfigKey: keyZeroEntropyAPIKey}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultZeroEntropyURL
	}
	model := cfg.Model
	if model == "" {
		model = DefaultZeroEntropyModel
	}
	return &zeroEntropyBackend{
		http:          newHTTPBackend("zeroentropy", baseURL, cfg.Timeout, cfg.Retry),
		apiKey:        cfg.APIKey,
		model:         model,
		dimensions:    resolveDimensions(cfg),
		vendorDims:    cfg.Dimensions,
		batchSize:     resolveBatch(cfg, DefaultBatchSize),
		maxConcurrent: resolveMaxConcurrent(cfg),
	}, nil
}
