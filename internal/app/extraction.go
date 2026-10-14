package app

import (
	"context"

	"github.com/studyzy/wenyuan/internal/extraction"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/reflect"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newReflectRunner builds the model-driven reflection loop. A nil provider, or
// the no-model selection, yields a nil runner, which storage reads as "use the
// deterministic in-process reflection".
func newReflectRunner(p provider.Provider) storage.ReflectRunner {
	if p == nil || p.Name() == none.NameNone {
		return nil
	}
	return reflect.NewRunner(p)
}

// rerankScorer adapts the provider's cross-encoder onto the recall pipeline's
// boundary, so the storage area reaches a rerank score without importing a
// provider.
type rerankScorer struct {
	reranker provider.Reranker
}

// Rerank scores the candidate texts against the query, in candidate order.
func (r rerankScorer) Rerank(ctx context.Context, query string, texts []string) ([]float64, error) {
	result, err := r.reranker.Rerank(ctx, provider.RerankRequest{Query: query, Documents: texts})
	if err != nil {
		return nil, err
	}
	return result.Scores, nil
}

// newRecallReranker builds the recall reranker from the provider boundary. A
// provider with no reranking capability, the no-model selection, or a nil
// provider yields a nil reranker, which leaves recall on its best-per-arm
// fallback.
func newRecallReranker(p provider.Provider) memoryops.RecallReranker {
	if p == nil || p.Name() == none.NameNone {
		return nil
	}
	reranker := provider.ProviderReranker(p)
	if reranker == nil {
		return nil
	}
	return rerankScorer{reranker: reranker}
}

// factExtractor adapts the extraction engine onto the storage area's fact
// boundary.
//
// The adapter lives here rather than in either area because it is the only
// place that knows both: the extraction engine speaks the provider's types and
// the storage area must not import a model provider, so the translation is a
// composition-root concern.
type factExtractor struct {
	extractor *extraction.Extractor
}

// newFactExtractor builds the adapter. A nil provider, or the no-model
// selection, yields a nil adapter, which storage reads as "store text
// verbatim".
func newFactExtractor(p provider.Provider) storage.FactExtraction {
	extractor := extraction.New(p)
	if !extractor.Available() {
		return nil
	}
	return factExtractor{extractor: extractor}
}

// Extract turns one item's text into the facts storage should store.
func (f factExtractor) Extract(ctx context.Context, text string) ([]storage.ExtractedFact, error) {
	result, err := f.extractor.Extract(ctx, text)
	if err != nil {
		return nil, err
	}
	facts := make([]storage.ExtractedFact, 0, len(result.Facts))
	for _, fact := range result.Facts {
		// An extracted name is the extractor's guess at who is named, not a
		// label the caller authored, so it carries no type and is always
		// resolved. Storage applies its own default label.
		entities := make([]storage.RetainEntity, 0, len(fact.Entities))
		for _, name := range fact.Entities {
			entities = append(entities, storage.RetainEntity{Text: name})
		}
		causes := make([]int, 0, len(fact.CausalRelations))
		for _, relation := range fact.CausalRelations {
			causes = append(causes, relation.TargetIndex)
		}
		facts = append(facts, storage.ExtractedFact{
			Text:            fact.Text,
			Entities:        entities,
			OccurredStart:   fact.OccurredStart,
			OccurredEnd:     fact.OccurredEnd,
			Confidence:      fact.Confidence,
			CausedByIndexes: causes,
		})
	}
	return facts, nil
}

// NewReflectRunner is the exported form of the reflection-loop constructor, so
// a binary other than the server can wire the same loop without duplicating the
// provider-selection rule.
func NewReflectRunner(p provider.Provider) storage.ReflectRunner {
	return newReflectRunner(p)
}
