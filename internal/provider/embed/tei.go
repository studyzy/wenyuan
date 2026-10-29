package embed

import (
	"context"
	"fmt"

	"github.com/studyzy/wenyuan/internal/provider"
)

// teiEmbedRequest is the request body a Text Embeddings Inference server
// accepts.
type teiEmbedRequest struct {
	Inputs []string `json:"inputs"`
}

// teiBackend serves the Text Embeddings Inference member. It is a plain
// text-in/vector-out endpoint that answers with a bare JSON array, one vector
// per input.
type teiBackend struct {
	http          *httpBackend
	dimensions    int
	batchSize     int
	maxConcurrent int
	passagePrefix string
	queryPrefix   string
}

// Name is the configured provider name.
func (b *teiBackend) Name() string { return "tei" }

// Model is empty: TEI serves whichever model its endpoint was launched with,
// so no identifier travels with a request.
func (b *teiBackend) Model() string { return "" }

// Dimensions is the width this backend guarantees for every vector.
func (b *teiBackend) Dimensions() int { return b.dimensions }

// Embed generates one vector per input text, in input order, applying the
// configured prefix for the request kind.
func (b *teiBackend) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
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

// fetch issues one batch request and returns the bare array of vectors.
func (b *teiBackend) fetch(ctx context.Context, texts []string) ([][]float32, error) {
	raw, err := b.http.postJSON(ctx, "/embed", teiEmbedRequest{Inputs: texts}, nil)
	if err != nil {
		return nil, err
	}
	var vectors [][]float32
	if err := decodeJSON(raw, &vectors, "cannot decode embedding response"); err != nil {
		return nil, err
	}
	if err := onePerInput(vectors, len(texts), "tei"); err != nil {
		return nil, err
	}
	return vectors, nil
}

// newTEI builds the Text Embeddings Inference member from the resolved
// configuration. The endpoint carries no credential but must be named.
func newTEI(cfg Config) (Embedder, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("embed: tei requires %s", keyTEIURL)
	}
	return &teiBackend{
		http:          newHTTPBackend("tei", cfg.BaseURL, cfg.Timeout, cfg.Retry),
		dimensions:    resolveDimensions(cfg),
		batchSize:     resolveBatch(cfg, DefaultTEIBatchSize),
		maxConcurrent: resolveMaxConcurrent(cfg),
		passagePrefix: cfg.PassagePrefix,
		queryPrefix:   cfg.QueryPrefix,
	}, nil
}
