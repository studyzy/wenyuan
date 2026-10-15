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

// MaxEmbeddingInputChars bounds how many characters of a unit's text are sent
// to the embedding provider in one request.
//
// Every provider caps the length of a single input, and a remote provider
// rejects an over-long one outright rather than truncating it server-side, so
// one long unit would fail the whole retain or recall batch. The bound is
// measured in characters because this boundary deliberately carries no
// tokenizer (that would add a model dependency to storage); it is set to the
// common 8192-token provider ceiling, which 8192 characters can never exceed
// even for the densest scripts where one character is about one token, while
// English text of that length stays far under it. The stored unit text is not
// truncated — only the string handed to the provider is — so recall still reads
// the whole unit.
const MaxEmbeddingInputChars = 8192

// truncateForEmbedding returns text cut to MaxEmbeddingInputChars on a rune
// boundary, so a multi-byte character is never split into invalid bytes. A text
// already within the bound is returned unchanged.
func truncateForEmbedding(text string) string {
	if len(text) <= MaxEmbeddingInputChars {
		return text
	}
	// len is bytes; cut on a rune boundary by counting runes up to the bound.
	count := 0
	for index := range text {
		if count == MaxEmbeddingInputChars {
			return text[:index]
		}
		count++
	}
	return text
}

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
// Several units are embedded in one call, and each input is capped at
// MaxEmbeddingInputChars first, so a single long unit cannot fail the batch.
func embedContents(ctx context.Context, generator EmbeddingGenerator, contents []itemContent) error {
	if generator == nil || len(contents) == 0 {
		return nil
	}
	texts := make([]string, 0, len(contents))
	for _, content := range contents {
		texts = append(texts, truncateForEmbedding(indexedEmbeddingText(content.unit, content.entities)))
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

// embedObservation returns the vector for an observation's text, or nil when no
// generator is configured or the embedding call fails. A failure is not fatal:
// an unembedded observation is still stored and reachable by keyword, which is
// better than losing the synthesis to a transient provider error.
func embedObservation(ctx context.Context, generator EmbeddingGenerator, text string) []float32 {
	if generator == nil || text == "" {
		return nil
	}
	vectors, err := generator.EmbedPassages(ctx, []string{truncateForEmbedding(text)})
	if err != nil || len(vectors) != 1 || len(vectors[0]) == 0 {
		return nil
	}
	return vectors[0]
}
