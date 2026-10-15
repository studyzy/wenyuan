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
// unit's stored text: the reference anchors each memory to the wall clock at
// retain ("(happened in <Month> <Year>)") and appends the entity mentions, so
// the indexed vector differs from the text a reader sees. The vector recall arm
// scores against this representation, which is why a fact's semantic score moves
// when its entities change even though its text does not.
func indexedEmbeddingText(text string, entities []RetainEntity, now time.Time) string {
	indexed := text + " (happened in " + now.Format("January 2006") + ")"
	if len(entities) > 0 {
		names := make([]string, 0, len(entities))
		for _, entity := range entities {
			names = append(names, entity.Text)
		}
		indexed += " " + strings.Join(names, " ")
	}
	return indexed
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
	now := time.Now()
	for _, content := range contents {
		texts = append(texts, truncateForEmbedding(indexedEmbeddingText(content.unit.Text, content.entities, now)))
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
