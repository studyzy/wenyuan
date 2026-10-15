package storage

import (
	"context"
	"time"
)

// Recall budget levels accepted by the recall request. The stored string
// values are the compatibility surface.
const (
	BudgetLow  = "low"
	BudgetMid  = "mid"
	BudgetHigh = "high"
)

// How many candidates each budget level lets the pipeline consider. The level
// is a bound on retrieval work, so it decides how many results a query may
// surface: a low-budget query that returned as much as a high-budget one would
// make the level meaningless.
const (
	BudgetLowLimit  = 100
	BudgetMidLimit  = 300
	BudgetHighLimit = 1000
)

// BudgetLimit maps a budget level to the candidate bound it allows. An absent
// or unrecognised level falls back to the middle level, which is what an
// omitted budget means.
func BudgetLimit(budget string) int {
	switch budget {
	case BudgetLow:
		return BudgetLowLimit
	case BudgetHigh:
		return BudgetHighLimit
	default:
		return BudgetMidLimit
	}
}

// RecallBudgetLevels returns the accepted budget levels in stable order.
func RecallBudgetLevels() []string { return []string{BudgetLow, BudgetMid, BudgetHigh} }

// Tag-matching modes accepted by the recall request. The stored string values
// are the compatibility surface.
const (
	TagsMatchAny       = "any"
	TagsMatchAll       = "all"
	TagsMatchAnyStrict = "any_strict"
	TagsMatchAllStrict = "all_strict"
	TagsMatchExact     = "exact"
)

// RecallTagMatchModes returns the accepted tag-matching modes in stable order.
func RecallTagMatchModes() []string {
	return []string{TagsMatchAny, TagsMatchAll, TagsMatchAnyStrict, TagsMatchAllStrict, TagsMatchExact}
}

// Tag-group leaf resolution options. The string values are the compatibility
// surface, so "exact" and "fuzzy" are the reference's own tokens.
const (
	// TagResolveExact compares a leaf's tags literally, which is what an
	// absent resolution means.
	TagResolveExact = "exact"
	// TagResolveFuzzy resolves a leaf's tags by similarity instead.
	TagResolveFuzzy = "fuzzy"
)

// Recall retrieval arm identifiers. These are internal ranking labels, not
// public surface; they name the arms a recall fans out to.
const (
	ArmSemantic = "semantic"
	ArmKeyword  = "keyword"
	ArmGraph    = "graph"
	ArmTemporal = "temporal"
	ArmEntity   = "entity"
)

// ArmReranker is not a retrieval arm: it names the optional cross-encoder pass
// in the same disabled list, so a bank's "turn reranking off" travels the same
// channel as "turn the keyword arm off" and the caller has one thing to check.
//
// Disabling it downgrades the ranking to the fused order rather than failing:
// the cross-encoder is an optional precision pass, and a request for answers
// without it is still a request for answers.
const ArmReranker = "reranker"

// RecallArms returns every retrieval arm identifier in stable order.
func RecallArms() []string {
	return []string{ArmSemantic, ArmKeyword, ArmGraph, ArmTemporal, ArmEntity}
}

// graphSeedMinSimilarity is the dense-similarity floor at which a semantic hit
// becomes an entry point for the graph walk. It mirrors the reference's
// graph_seed_min_similarity default.
const graphSeedMinSimilarity = 0.3

// graphSeedsAllowed reports whether the graph arm may run for this query.
//
// The reference derives the graph arm's entry points from the semantic arm's
// dense hits, and it only does so while the caller's semantic floor leaves room
// for them: once a floor above graphSeedMinSimilarity is set, the arm is starved
// of seeds and contributes nothing. Reproducing that starvation is what stops a
// fact the caller floored out of the semantic arm from being resurrected
// through a link from a survivor — which is the only way retrieval-level floors
// remove a fact, and the behaviour the min_scores contract pins.
func graphSeedsAllowed(q RecallQuery) bool {
	floor, ok := q.MinScores[ArmSemantic]
	return !ok || floor <= graphSeedMinSimilarity
}

// TemporalWindow is a caller-supplied inclusive window used for temporal
// proximity ranking. Bounds are inclusive; a zero bound is treated as absent.
type TemporalWindow struct {
	Start    time.Time
	End      time.Time
	HasStart bool
	HasEnd   bool
}

// Contains reports whether the interval [start, end] overlaps the window.
// A missing bound is treated as unbounded on that side.
func (w TemporalWindow) Contains(start, end *time.Time) bool {
	if start == nil && end == nil {
		return false
	}
	var lo, hi time.Time
	switch {
	case start != nil && end != nil:
		lo, hi = *start, *end
	case start != nil:
		lo, hi = *start, *start
	default:
		lo, hi = *end, *end
	}
	if hi.Before(lo) {
		lo, hi = hi, lo
	}
	if w.HasStart && hi.Before(w.Start) {
		return false
	}
	if w.HasEnd && lo.After(w.End) {
		return false
	}
	return true
}

// RecallQuery is the full recall read request after decoding and validation.
// It carries every optional field the pipeline consumes.
type RecallQuery struct {
	BankID string
	Query  string
	// Types is the resolved fact-type selection; empty means every supported
	// type.
	Types []string
	// PreferObservations drops raw facts a returned observation superseded.
	PreferObservations bool
	// Budget is the retrieval work bound (low, mid, high).
	Budget string
	// MaxTokens bounds the returned fact text; the storage layer reports the
	// ranked matches and the handler applies the cap, but the cap is threaded
	// through so arms can bound work.
	MaxTokens int
	// MinScores holds per-stage score floors.
	MinScores map[string]float64
	// Tags and TagsMatch express flat tag scoping.
	Tags      []string
	TagsMatch string
	// TagGroups expresses compound tag scoping; mutually exclusive with Tags.
	TagGroups []TagGroup
	// Temporal is the caller-supplied window; used verbatim.
	Temporal *TemporalWindow
	// Include flags the optional side channels.
	IncludeEntities    bool
	IncludeChunks      bool
	IncludeSourceFacts bool
	// QueryTimestamp is the query-time anchor used for recency scoring, when
	// supplied.
	QueryTimestamp *time.Time
	// QueryEmbedding is the embedded query vector. When present, the semantic
	// arm ranks by cosine similarity against the stored embedding instead of
	// the deterministic lexical stand-in. When absent, the semantic arm falls
	// back to that stand-in so a deployment with no embedding provider still
	// retrieves.
	QueryEmbedding []float32
	// StrategyBoosts maps an arm identifier to a bounded boost level
	// (low, medium, high). Empty leaves every arm unboosted.
	StrategyBoosts map[string]string
	// DisabledArms lists arms removed by bank configuration; those arms are
	// omitted from retrieval entirely.
	DisabledArms []string
	// Limit and Offset page the ranked result list before envelope assembly.
	Limit  int
	Offset int
	// Rerank, when non-nil, rescoring of the fused candidates runs through
	// this cross-encoder boundary instead of the best-per-arm fallback. It is
	// called once with every fused candidate's text, in candidate order, and
	// must return one score per candidate in the same order. An error leaves
	// the fallback in place rather than failing the recall.
	Rerank RerankFunc
	// RerankDepth bounds how many candidates reach the reranker, in ranked
	// order. Zero means every candidate does.
	RerankDepth int
	// RerankRankPosition reports that Rerank is a rank-position reranker: one
	// whose scores echo a candidate's position rather than measuring calibrated
	// relevance. Such a score is neither published in a result's score map nor
	// usable as a relevance floor, because the top candidate would read 1.0 on
	// every recall however weak it is (see memoryops's
	// RerankerScoresByRankPosition).
	RerankRankPosition bool
	// RerankSeedFromFused reports that the reranker's returned values carry no
	// ordering information of their own — the process knows this when its
	// configured reranker is the RRF passthrough. When set, the fused ordering
	// is seeded directly instead of min-max normalising those values. See
	// passthroughRerankSeed.
	RerankSeedFromFused bool
	// RerankPrunesCandidates reports that Rerank judges relevance rather than
	// only ordering it, marking a candidate it would remove with a score of
	// exactly 0.0. When set, candidates scored 0.0 are dropped from the result
	// instead of being carried to the token budget; a candidate the reranker
	// left unscored keeps its fused score and is never pruned.
	RerankPrunesCandidates bool
	// Fusion selects how the per-arm result lists are merged into one order.
	// Empty (and "rrf") sums reciprocal ranks, which is the default; the
	// "interleave" mode round-robins the arms instead, guaranteeing each arm's
	// top hits a slot. The interleave mode is what dedup-style recall needs: an
	// existing near-identical observation has no graph link and little lexical
	// overlap, so the sum buries the twin below the budget cutoff.
	Fusion string
	// Recency is the age-to-freshness tuning the combined score uses. Empty
	// means the historical linear curve.
	Recency RecencyDecayConfig
	// PerSourceCap truncates each retrieval arm to its best N results before
	// fusion, so one over-expanding backend cannot crowd the others out of the
	// merged pool. Zero (or negative) disables the cap.
	PerSourceCap int
}

// RerankFunc scores candidate texts against a query. It is a function type
// rather than an interface so the storage area needs no provider import and
// the caller can pass a closure that captures its own client.
type RerankFunc func(ctx context.Context, query string, texts []string) ([]float64, error)

// PartialRerankError reports that the cross-encoder scored part of its batch
// before failing or exceeding its time budget. Scores holds one entry per
// requested text in the same order; an entry is NaN where that candidate was
// never scored. The pipeline uses the finite scores and leaves the rest on
// their pre-rerank (fused) order and score, so a slow or flaky reranker still
// answers the recall instead of failing it.
//
// A RerankFunc returns this as the error half of its result: the finite prefix
// of Scores is what it managed to produce, and the remainder is NaN. Any other
// error, or a Scores slice of the wrong length, is treated as a total failure
// and falls back to the fused score throughout.
type PartialRerankError struct {
	Scores []float64
}

// Error implements error.
func (e *PartialRerankError) Error() string {
	return "reranker returned partial scores"
}

// TagGroup is one compound tag filter node: either a boolean node (And/Or/Not
// set) or a leaf with one tag and its resolution/match mode.
type TagGroup struct {
	// And matches every child; Or matches any child; Not matches none of its
	// single child's leaves.
	And []TagGroup
	Or  []TagGroup
	Not []TagGroup
	// Leaf fields.
	Tag     string
	Tags    []string
	Resolve string
	Match   string
}

// RecallScores holds the per-stage retrieval and reranker scores. A stage that
// did not surface the result reports no value. The set is the reference's
// recall envelope: final, reranker, semantic and keyword.
type RecallScores struct {
	Semantic *float64
	Keyword  *float64
	Reranker *float64
}

// RecallTraceDetail is the reference-shaped retrieval trace. The shape mirrors
// the reference's SearchTrace: one entry per (arm, fact type) for the retrieval
// arms, the RRF merge of the candidates, and the reranked list with the score
// components the combined score was built from.
type RecallTraceDetail struct {
	// Anchor is the time the recency signal was measured against: the caller's
	// query timestamp when supplied, otherwise the execution time. It is
	// reported rather than re-derived so a reader can see the input that
	// produced a ranking.
	Anchor time.Time
	// Budget is the search depth the request was configured with (the budget
	// level's candidate bound), which is the dial the trace's query block
	// reports rather than the number of candidates it happened to find.
	Budget int
	// MaxTokens, Tags and TagsMatch echo the recall request.
	MaxTokens int
	Tags      []string
	TagsMatch string
	// TemporalStart and TemporalEnd are the window the query was constrained
	// to, when it named one.
	TemporalStart *time.Time
	TemporalEnd   *time.Time
	// RetrievalResults is one entry per retrieval arm and fact type, including
	// an empty entry for an arm that found nothing of that type.
	RetrievalResults []TraceArmResults
	// RRFMerged is the fused candidate list, best first.
	RRFMerged []TraceRRFEntry
	// Reranked is the ranked result list with the components behind each score.
	Reranked []TraceRerankedEntry
}

// TraceArmResults is one retrieval arm's results for one fact type.
type TraceArmResults struct {
	// MethodName is the reference's arm name (semantic, bm25, graph, temporal).
	MethodName string
	// FactType is the fact type this arm searched (world, experience,
	// observation).
	FactType string
	Results  []TraceArmResult
}

// TraceArmResult is one arm hit.
type TraceArmResult struct {
	Rank      int
	NodeID    string
	Text      string
	Context   string
	EventDate *time.Time
	FactType  string
	Score     float64
	ScoreName string
}

// TraceRRFEntry is one fused candidate in the RRF merge.
type TraceRRFEntry struct {
	NodeID       string
	Text         string
	RRFScore     float64
	SourceRanks  map[string]int
	FinalRRFRank int
}

// TraceRerankedEntry is one result after reranking, with its score components.
type TraceRerankedEntry struct {
	NodeID          string
	Text            string
	RerankScore     float64
	RerankRank      int
	RRFRank         int
	RankChange      int
	ScoreComponents map[string]float64
}

// RecallAttachment is an optional recall-result attachment.
type RecallAttachment struct {
	ID   string
	Kind string
	URI  string
	// Hash, MediaType, ByteSize and Filename describe the bytes behind the id.
	// They are what the reflect citation's attachment shape carries, so a caller
	// can render or verify the image without a second fetch.
	Hash      string
	MediaType string
	ByteSize  int64
	Filename  string
}

// EntityState is one entry of the entity-state side channel, keyed by entity
// name at the envelope layer.
type EntityState struct {
	Name          string
	ID            string
	CanonicalName string
	Observations  []EntityObservation
}

// EntityObservation is one observation carried on an entity-state entry.
type EntityObservation struct {
	Text        string
	MentionedAt *time.Time
}

// ChunkEntry is one entry of the chunk side channel, keyed by chunk id at the
// envelope layer.
type ChunkEntry struct {
	ID        string
	Text      string
	Index     int
	Truncated bool
}
