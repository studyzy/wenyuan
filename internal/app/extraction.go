package app

import (
	"context"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/extraction"
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
// verbatim". correctionRetries bounds how many times a response whose facts came
// back in a foreign shape is re-asked with the extractor's key correction.
// ExtractionSettings are the deployment's extraction knobs. They travel as one
// value so the synchronous path, the queued path and the worker entrypoint
// cannot drift apart.
type ExtractionSettings struct {
	// CorrectionRetries bounds how many times a response in a foreign shape is
	// re-asked with the drift correction.
	CorrectionRetries int
	// MaxCompletionTokens caps the extraction call's generated tokens; zero
	// leaves the parameter unset.
	MaxCompletionTokens int
}

// ExtractionSettingsFrom resolves the settings a configuration carries.
func ExtractionSettingsFrom(cfg config.Config) ExtractionSettings {
	return ExtractionSettings{
		CorrectionRetries:   cfg.Provider.RetainMaxRetries,
		MaxCompletionTokens: cfg.RetainTuning.MaxCompletionTokens,
	}
}

func newFactExtractor(p provider.Provider, settings ExtractionSettings) storage.FactExtraction {
	extractor := extraction.New(p)
	if !extractor.Available() {
		return nil
	}
	extractor.MaxCorrectionRetries = settings.CorrectionRetries
	extractor.MaxCompletionTokens = settings.MaxCompletionTokens
	return factExtractor{extractor: extractor}
}

// Extract turns one item's text into the facts storage should store.
func (f factExtractor) Extract(ctx context.Context, text string) ([]storage.ExtractedFact, error) {
	return f.ExtractWithOptions(ctx, text, storage.FactExtractionOptions{})
}

// ExtractWithOptions runs the extraction with the request's retain
// configuration: the bank's mission and custom instructions, the item's
// strategy, and the reference time a relative phrase in the text resolves
// against.
func (f factExtractor) ExtractWithOptions(ctx context.Context, text string, opts storage.FactExtractionOptions) ([]storage.ExtractedFact, error) {
	result, err := f.extractor.ExtractWith(ctx, text, extractionOptions(opts))
	if err != nil {
		return nil, err
	}
	return toStorageFacts(result.Facts), nil
}

// ExtractDryRun runs the extraction for the dry-run preview, reporting the
// token usage alongside the candidate facts. It is the same call as
// ExtractWithOptions; only the usage, which the storage boundary drops, is
// carried out.
func (f factExtractor) ExtractDryRun(ctx context.Context, text string, opts storage.FactExtractionOptions) (storage.DryRunExtraction, error) {
	result, err := f.extractor.ExtractWith(ctx, text, extractionOptions(opts))
	if err != nil {
		return storage.DryRunExtraction{}, err
	}
	return storage.DryRunExtraction{
		Facts: toStorageFacts(result.Facts),
		Usage: storage.TokenUsage{
			InputTokens:    result.Usage.InputTokens,
			OutputTokens:   result.Usage.OutputTokens,
			TotalTokens:    result.Usage.TotalTokens,
			CachedTokens:   result.Usage.CachedInputTokens,
			ThoughtsTokens: result.Usage.ReasoningTokens,
		},
	}, nil
}

// extractionOptions maps the storage-shaped per-item settings onto the
// extraction engine's options.
func extractionOptions(opts storage.FactExtractionOptions) extraction.Options {
	attachments := make([]extraction.Attachment, 0, len(opts.Attachments))
	for _, attachment := range opts.Attachments {
		attachments = append(attachments, extraction.Attachment{
			ShortID:   attachment.ShortID,
			MediaType: attachment.MediaType,
			Kind:      attachment.Kind,
			Data:      attachment.Data,
		})
	}
	return extraction.Options{
		Mode:                   opts.Mode,
		Mission:                opts.Mission,
		Instructions:           opts.Instructions,
		Strategy:               opts.Strategy,
		ReferenceTime:          opts.ReferenceTime,
		OptionalFactDimensions: opts.OptionalFactDimensions,
		Vocabulary:             opts.Vocabulary,
		Attachments:            attachments,
		ExtractCausalLinks:     opts.ExtractCausalLinks,
	}
}

// toStorageFacts maps the extractor's facts onto the storage shape.
func toStorageFacts(facts []extraction.Fact) []storage.ExtractedFact {
	out := make([]storage.ExtractedFact, 0, len(facts))
	for _, fact := range facts {
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
		out = append(out, storage.ExtractedFact{
			Text:            fact.Text,
			Labels:          fact.Labels,
			Where:           fact.Where,
			Kind:            fact.Kind,
			Type:            fact.Type,
			FromAttachments: fact.FromAttachments,
			Entities:        entities,
			OccurredStart:   fact.OccurredStart,
			OccurredEnd:     fact.OccurredEnd,
			Confidence:      fact.Confidence,
			CausedByIndexes: causes,
		})
	}
	return out
}

// NewReflectRunner is the exported form of the reflection-loop constructor, so
// a binary other than the server can wire the same loop without duplicating the
// provider-selection rule.
func NewReflectRunner(p provider.Provider) storage.ReflectRunner {
	return newReflectRunner(p)
}
