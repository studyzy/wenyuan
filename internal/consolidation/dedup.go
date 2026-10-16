package consolidation

import (
	"context"
	"math"
)

// Near-duplicate detection for consolidation.
//
// The reference decides whether a create (or a rewritten update) merely
// restates an observation already on record by the cosine similarity of the two
// observation embeddings: it embeds the candidate text, probes the observations
// of the same scope, and only a neighbour whose cosine reaches the configured
// threshold is treated as the same conclusion — which is then folded into that
// neighbour rather than written as a second row. The threshold is the
// deployment's consolidation.dedup threshold; at its default (0.97) the feature
// is enabled, and a threshold of 1 disables it, since no two distinct vectors
// reach cosine 1.
//
// A deployment with no embedding model cannot run that comparison. For it the
// pass keeps its older token-Jaccard floor, so a restated conclusion is still
// caught instead of silently accumulating. The two mechanisms live on different
// scales, so exactly one of them decides: when embeddings are available the
// cosine threshold decides alone and the floor is not consulted.

// ObservationDuplicateStore is the optional capability a store provides when a
// deployment can embed. The store embeds the candidate observation text and
// returns that embedding together with the in-scope observations that carry a
// stored embedding, nearest first. candidates is bounded by limit so the caller
// sees the likeliest twins.
//
// It reports embedded=false — with no error — when there is no embedding model,
// or when the text produced no vector, which is the signal to the pass that the
// cosine mechanism cannot decide and the text floor must.
type ObservationDuplicateStore interface {
	ObservationDuplicates(ctx context.Context, bankID string, scope []string, text string, limit int) (probe []float32, candidates []Observation, embedded bool, err error)
}

// duplicateObservation reports whether a candidate observation text repeats one
// already on record, returning the twin it matched when the embedding path
// found one, so the caller can fold the candidate into it instead of creating a
// second conclusion.
//
// The reference's mechanism decides whenever the store can embed: the nearest
// in-scope observation reached by the candidate's embedding is a duplicate when
// its cosine is at or above the configured dedup threshold, and nothing else is
// consulted. Without embeddings — a deployment with no embedding model, or a
// store that cannot probe — the pass falls back to its token-similarity floor
// over the offered observation keys, so dedup still runs rather than stopping.
func (c *Consolidator) duplicateObservation(ctx context.Context, bankID string, scope []string, text, normalized string, fallbackKeys []string) (Observation, bool) {
	// A threshold of 1 disables dedup outright, matching the reference's
	// _dedup_active gate: no distinct pair of embeddings reaches cosine 1, so
	// running the probe (or the floor) could only ever waste work or, for the
	// floor, act on a different scale than the operator asked for.
	if c.tuning.DedupThreshold >= 1 {
		return Observation{}, false
	}
	if detector, ok := c.store.(ObservationDuplicateStore); ok {
		probe, candidates, embedded, err := detector.ObservationDuplicates(ctx, bankID, scope, text, DefaultRelatedObservations)
		if err == nil && embedded {
			for _, candidate := range candidates {
				if len(candidate.Embedding) == 0 {
					continue
				}
				if cosineSimilarity(probe, candidate.Embedding) >= c.tuning.DedupThreshold {
					return candidate, true
				}
			}
			// Embeddings decided. A candidate that stayed under the threshold is
			// a genuinely different conclusion, so the text floor must not
			// second-guess the probe on a different scale.
			return Observation{}, false
		}
		// No embedding model, or a probe that failed: lose a dedup rather than
		// fail the pass, and keep the floor so behaviour does not regress on a
		// deployment that cannot embed.
	}
	if nearDuplicate(normalized, fallbackKeys) {
		return Observation{}, true
	}
	return Observation{}, false
}

// cosineSimilarity returns the cosine of two equal-width vectors, in [-1, 1].
// Vectors of differing widths, or with a zero magnitude, score 0 rather than
// producing a NaN, so an unusable pair can never spuriously clear a threshold.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
