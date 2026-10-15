package app

import (
	"context"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/embed"
	"github.com/studyzy/wenyuan/internal/storage"
)

// embeddingGenerator adapts the configured embedding backend onto the storage
// embedding boundary, so the storage area embeds passages and queries without
// importing a model provider.
type embeddingGenerator struct {
	embedder embed.Embedder
}

// EmbedPassages embeds stored memory texts, one vector per input in order.
func (g embeddingGenerator) EmbedPassages(ctx context.Context, texts []string) ([][]float32, error) {
	result, err := g.embedder.Embed(ctx, provider.EmbeddingRequest{
		Texts: texts,
		Kind:  provider.EmbeddingPassage,
	})
	if err != nil {
		return nil, err
	}
	return result.Vectors, nil
}

// EmbedQuery embeds a recall query.
func (g embeddingGenerator) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	result, err := g.embedder.Embed(ctx, provider.EmbeddingRequest{
		Texts: []string{text},
		Kind:  provider.EmbeddingQuery,
	})
	if err != nil {
		return nil, err
	}
	if len(result.Vectors) == 0 {
		return nil, nil
	}
	return result.Vectors[0], nil
}

// embeddingGeneratorFrom returns the storage embedding boundary when an
// embedding backend is configured. A nil backend (no embedding provider, or a
// provider that could not be built) yields nil, which leaves every unit
// unembedded and recall on its non-vector arms. The backend is resolved once at
// composition, so an unusable configuration surfaces at startup rather than at
// first write.
func embeddingGeneratorFrom(embedder embed.Embedder) storage.EmbeddingGenerator {
	if embedder == nil {
		return nil
	}
	return embeddingGenerator{embedder: embedder}
}

// ResolveEmbedder exposes the embedder resolution to the worker entrypoint,
// which builds the same embedding backend the server would so a queued job
// embeds with the deployment's configured provider.
func ResolveEmbedder(cfg config.Config) (embed.Embedder, error) {
	return resolveEmbedder(cfg)
}

// EmbeddingGeneratorFrom exposes the storage embedding boundary for callers
// outside the app package that already hold an embedder.
func EmbeddingGeneratorFrom(embedder embed.Embedder) storage.EmbeddingGenerator {
	return embeddingGeneratorFrom(embedder)
}

// setObservationEmbedder gives the store the generator the consolidation write
// path embeds observation texts with, so an observation lands with a vector and
// the vector recall arm can rank it rather than reaching the synthesis only by
// keyword. A store without the capability (or a deployment with no embedding
// model) is left as it is.
func setObservationEmbedder(store storage.Store, generator storage.EmbeddingGenerator) {
	if setter, ok := store.(interface {
		SetObservationEmbedder(storage.EmbeddingGenerator)
	}); ok {
		setter.SetObservationEmbedder(generator)
	}
}
