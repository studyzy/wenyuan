package storage

import "time"

// Recall budget levels accepted by the recall request. The stored string
// values are the compatibility surface.
const (
	BudgetLow  = "low"
	BudgetMid  = "mid"
	BudgetHigh = "high"
)

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

// Tag-group leaf resolution options. The stored string values are the
// compatibility surface.
const (
	TagResolveLiteral = "literal"
	TagResolveFuzzy   = "fuzzy"
)

// Recall retrieval arm identifiers. These are internal ranking labels, not
// public surface; they name the arms a recall fans out to.
const (
	ArmSemantic = "semantic"
	ArmKeyword  = "keyword"
	ArmTemporal = "temporal"
	ArmEntity   = "entity"
)

// RecallArms returns every retrieval arm identifier in stable order.
func RecallArms() []string { return []string{ArmSemantic, ArmKeyword, ArmTemporal, ArmEntity} }

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
	// StrategyBoosts maps an arm identifier to a bounded boost level
	// (low, medium, high). Empty leaves every arm unboosted.
	StrategyBoosts map[string]string
	// DisabledArms lists arms removed by bank configuration; those arms are
	// omitted from retrieval entirely.
	DisabledArms []string
	// Limit and Offset page the ranked result list before envelope assembly.
	Limit  int
	Offset int
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
// did not surface the result reports no value.
type RecallScores struct {
	Semantic *float64
	Keyword  *float64
	Reranker *float64
}

// RecallAttachment is an optional recall-result attachment.
type RecallAttachment struct {
	ID   string
	Kind string
	URI  string
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
