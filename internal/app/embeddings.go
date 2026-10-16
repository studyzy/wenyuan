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

// derefIntOrZero reads an optional integer as a plain one, where zero means
// "unset" for the consumer.
func derefIntOrZero(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

// setStoreTuning installs the resolved tuning on a store that carries it, so a
// deployment's retrieval, write and reflection knobs reach the pipeline instead
// of the package defaults. A store without the capability is left as it is.
func setStoreTuning(store storage.Store, cfg config.Config) {
	setter, ok := store.(interface{ SetTuning(storage.Tuning) })
	if !ok {
		return
	}
	setter.SetTuning(storage.Tuning{
		GraphSeedMinSimilarity:       cfg.RecallTuning.GraphSeedMinSimilarity,
		SemanticLinkMinSimilarity:    cfg.RecallTuning.SemanticLinkMinSimilarity,
		BM25MaxQueryTerms:            cfg.RecallTuning.BM25MaxQueryTerms,
		RecallBudgetLow:              cfg.ProcessDefaults.RecallBudgetFixedLow,
		RecallBudgetMid:              cfg.ProcessDefaults.RecallBudgetFixedMid,
		RecallBudgetHigh:             cfg.ProcessDefaults.RecallBudgetFixedHigh,
		ReflectMaxIterations:         cfg.ReflectTuning.MaxIterations,
		ReflectContextBudget:         cfg.ReflectTuning.MaxContextTokens,
		EntityMergeMinSimilarity:     cfg.EntityTuning.MergeMinSimilarity,
		MaxQueryTokens:               cfg.RecallTuning.MaxQueryTokens,
		MaxConcurrent:                cfg.RecallTuning.MaxConcurrent,
		ConnectionBudget:             cfg.RecallTuning.ConnectionBudget,
		BM25SelectiveTerms:           cfg.RecallTuning.BM25SelectiveTerms,
		InternalRecallIncludeChunks:  cfg.ProcessDefaults.RecallIncludeChunks,
		InternalRecallMaxFactTokens:  cfg.ProcessDefaults.RecallMaxTokens,
		InternalRecallMaxChunkTokens: cfg.ProcessDefaults.RecallChunksMaxTokens,
		ExtractCausalLinks:           cfg.RetainTuning.ExtractCausalLinks,
		EntityIntrabatchMerge:        cfg.EntityTuning.IntrabatchMerge,
		ReflectPromptCacheEnabled:    cfg.ReflectTuning.PromptCacheEnabled,
		ReflectMaxCompletionTokens:   derefIntOrZero(cfg.ReflectTuning.MaxCompletionTokens),
		RecallBudgetFunction:         cfg.ProcessDefaults.RecallBudgetFunction,
		RecallBudgetAdaptiveLow:      cfg.ProcessDefaults.RecallBudgetAdaptiveLow,
		RecallBudgetAdaptiveMid:      cfg.ProcessDefaults.RecallBudgetAdaptiveMid,
		RecallBudgetAdaptiveHigh:     cfg.ProcessDefaults.RecallBudgetAdaptiveHigh,
		RecallBudgetMin:              cfg.ProcessDefaults.RecallBudgetMin,
		RecallBudgetMax:              cfg.ProcessDefaults.RecallBudgetMax,
		DiagnosticPhases:             cfg.RecallTuning.DiagnosticPhases,
		PhaseSampleEvery:             cfg.RecallTuning.PhaseSampleEvery,
		Consolidation: storage.ConsolidationTuning{
			MemoryBatchSize:     cfg.ConsolidationTuning.MemoryBatchSize,
			DedupThreshold:      cfg.ConsolidationTuning.DedupThreshold,
			MaxAttempts:         cfg.ConsolidationTuning.MaxAttempts,
			MaxTokens:           cfg.ConsolidationTuning.MaxTokens,
			RecallBudget:        cfg.ConsolidationTuning.RecallBudget,
			MaxCompletionTokens: derefIntOrZero(cfg.ConsolidationTuning.MaxCompletionTokens),
		},
		ANNIterativeScan:           cfg.DBTuning.ANNIterativeScan,
		ANNMaxScanTuples:           cfg.DBTuning.ANNMaxScanTuples,
		EntityCandidateSimilarity:  cfg.EntityTuning.TRGMSimilarityThreshold,
		EntityResolutionCandidates: cfg.RetainTuning.EntityResolutionCandidates,
		ObservationHistory:         cfg.EnableObservationHistory,
		MentalModelHistory:         cfg.EnableMentalModelHistory,
		ObservationHistoryMax:      cfg.ConsolidationTuning.HistoryMaxEntries,
		MentalModelHistoryMax:      cfg.ConsolidationTuning.MentalModelHistoryEntries,
	})
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
