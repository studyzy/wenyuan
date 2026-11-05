package storage

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// EmbeddingGenerator turns text into vectors for the retain write path and the
// recall query.
//
// It is an interface so the storage area neither imports a model provider nor
// knows which one is configured: the composition root adapts its provider onto
// this shape, and a deployment with no embedding model leaves it nil, which
// keeps every unit unembedded and leaves recall on its non-vector arms.
type EmbeddingGenerator interface {
	// EmbedPassages embeds stored memory texts, one vector per input in order.
	EmbedPassages(ctx context.Context, texts []string) ([][]float32, error)
	// EmbedQuery embeds a recall query.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// The embedding boundary applies the deployment's per-input token cap before a
// text reaches any provider, charging the provider's own input prefix against
// that budget. This layer therefore hands the full indexed text to the
// generator unchanged and keeps the stored unit's whole text: pre-truncating
// here on a character measure would cut twice and would miss the prefix.

// indexedEmbeddingText renders the string a unit is indexed by. It is not the
// text that is stored or shown: it is what the semantic arm compares a query
// against and what the unit's search vector covers.
//
// It mirrors the reference's augment_texts_with_dates field for field, because
// the augmentation is itself a retrieval signal:
//
//   - The date is the unit's own — occurred_start, falling back to mentioned_at
//     — never the retain clock. A fact about 2021 embedded as the month it was
//     retained asserts a time the fact does not, and a query for "camping in
//     June" then cannot reach it.
//   - A span whose end differs from its start reads as a range, so a fact that
//     covers a period is reachable from either end of it.
//   - A unit with no date at all gets no date clause, rather than a clause
//     naming the clock.
//   - Entity names are bracketed and comma-joined, so they read as a distinct
//     field rather than as more of the sentence.
func indexedEmbeddingText(unit MemoryUnit, entities []RetainEntity) string {
	indexed := unit.Text
	start := unit.OccurredStart
	if start == nil {
		start = unit.MentionedAt
	}
	switch {
	case start == nil:
		// No date to name: the reference leaves the text unaugmented rather than
		// substituting one.
	case unit.OccurredEnd != nil && (unit.OccurredStart == nil || !unit.OccurredEnd.Equal(*unit.OccurredStart)):
		indexed += " (happened from " + formatReadableDate(*start) + " to " + formatReadableDate(*unit.OccurredEnd) + ")"
	default:
		indexed += " (happened in " + formatReadableDate(*start) + ")"
	}
	if len(entities) > 0 {
		names := make([]string, 0, len(entities))
		for _, entity := range entities {
			names = append(names, entity.Text)
		}
		indexed += " [" + strings.Join(names, ", ") + "]"
	}
	return indexed
}

// formatReadableDate renders a date the way the reference's own formatter does
// ("June 2024"), so a query phrased as a month and a year matches the text the
// index holds.
func formatReadableDate(at time.Time) string {
	return at.UTC().Format("January 2006")
}

// embedContents fills each content's unit embedding from the generator. A nil
// generator or an empty list is a no-op, which is what a deployment with no
// embedding model gets. A generator that returns the wrong number of vectors is
// an error rather than a silently misaligned write: a vector attached to the
// wrong unit is worse than no vector.
//
// Several units are embedded in one call. The full indexed text is handed to
// the generator: the embedding boundary owns the input cap, so storage does
// not pre-truncate on a different measure and the stored unit keeps its whole
// text.
func embedContents(ctx context.Context, generator EmbeddingGenerator, contents []itemContent) error {
	if generator == nil || len(contents) == 0 {
		return nil
	}
	// Attribute the embedder's time to the retain in scope, if there is one. A
	// no-op outside a retain and a phase that is never recorded when no
	// generator runs, so a deployment with no embedder reports no embed phase
	// rather than a zero-second one.
	stop := measureRetainPhase(ctx, "embed")
	defer stop()
	texts := make([]string, 0, len(contents))
	for _, content := range contents {
		texts = append(texts, indexedEmbeddingText(content.unit, content.entities))
	}
	vectors, err := generator.EmbedPassages(ctx, texts)
	if err != nil {
		return err
	}
	if len(vectors) != len(texts) {
		return fmt.Errorf("embedding generator returned %d vectors for %d texts", len(vectors), len(texts))
	}
	for i := range contents {
		if len(vectors[i]) > 0 {
			contents[i].unit.Embedding = vectors[i]
		}
	}
	return nil
}

// embedUnitText recomputes one memory unit's embedding the way retain does:
// the same date+entity augmentation (indexedEmbeddingText) over the unit's own
// text, so an edited fact embeds exactly as a freshly-retained one would. It is
// the editing counterpart of embedContents, which the retain path uses.
//
// A nil generator, a provider error, or an empty result yields nil, which
// leaves the unit's existing vector in place rather than clearing it: a failed
// re-embed must not silently drop a unit out of the vector arm.
func embedUnitText(ctx context.Context, generator EmbeddingGenerator, unit MemoryUnit, entityNames []string) []float32 {
	if generator == nil {
		return nil
	}
	entities := make([]RetainEntity, 0, len(entityNames))
	for _, name := range entityNames {
		if name != "" {
			entities = append(entities, RetainEntity{Text: name})
		}
	}
	texts := []string{indexedEmbeddingText(unit, entities)}
	vectors, err := generator.EmbedPassages(ctx, texts)
	if err != nil || len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil
	}
	return vectors[0]
}

// embedMentalModelText returns the vector for a mental model's searchable
// document — its name and content, the exact text the reference embeds for a
// model (f"{name} {content}"). A nil generator or a failed call yields nil, so a
// caller rebuilding the column writes SQL NULL, which is what the reference
// stores when it has no vector.
func embedMentalModelText(ctx context.Context, generator EmbeddingGenerator, name, content string) []float32 {
	if generator == nil {
		return nil
	}
	vectors, err := generator.EmbedPassages(ctx, []string{name + " " + content})
	if err != nil || len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil
	}
	return vectors[0]
}

// mentalModelEmbeddingArg embeds a model's document and returns the value to
// bind to the vector column, or nil for SQL NULL. A configured embedder whose
// vector does not match the deployment's dimension is an error rather than a
// silent NULL: the same guard PutMentalModel applies to a caller-supplied vector.
func mentalModelEmbeddingArg(ctx context.Context, generator EmbeddingGenerator, name, content string) (any, error) {
	vec := embedMentalModelText(ctx, generator, name, content)
	if len(vec) == 0 {
		return nil, nil
	}
	if err := ValidateEmbedding(vec); err != nil {
		return nil, err
	}
	return NullableEmbedding(vec), nil
}

// embedObservation returns the vector for an observation's text, or nil when no
// generator is configured or the embedding call fails. A failure is not fatal:
// an unembedded observation is still stored and reachable by keyword, which is
// better than losing the synthesis to a transient provider error.
func embedObservation(ctx context.Context, generator EmbeddingGenerator, text string) []float32 {
	if generator == nil || text == "" {
		return nil
	}
	vectors, err := generator.EmbedPassages(ctx, []string{text})
	if err != nil || len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil
	}
	return vectors[0]
}
