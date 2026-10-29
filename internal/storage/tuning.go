package storage

import (
	"context"
	"math"
	"strings"
	"time"
)

// Tuning carries the retrieval, write and reflection knobs a deployment
// resolved from its environment. It is a value on each store rather than a
// package constant so a deployment's configuration reaches the pipeline, and so
// the two backends can be constructed with the same numbers and agree.
//
// The zero value is not usable: DefaultTuning holds the reference's own
// defaults, and every store is constructed with it.
type Tuning struct {
	// GraphSeedMinSimilarity is the dense-similarity floor at which a semantic
	// hit becomes an entry point for the graph walk.
	GraphSeedMinSimilarity float64
	// SemanticLinkMinSimilarity is the cosine floor above which two units are
	// linked as semantically related when they are written together.
	SemanticLinkMinSimilarity float64
	// BM25MaxQueryTerms bounds how many query terms one BM25 tsquery carries.
	BM25MaxQueryTerms int
	// LinkExpansionPerEntityLimit caps how many targets one entity contributes
	// to the graph expansion, and LinkExpansionTimeout bounds that statement.
	LinkExpansionPerEntityLimit int
	LinkExpansionTimeout        time.Duration
	// SemanticMinSimilarity and TemporalSemanticMinSimilarity are the semantic
	// and temporal arms' default score floors, applied when the caller set none
	// for that arm; BM25MinScore is the keyword arm's.
	SemanticMinSimilarity         float64
	TemporalSemanticMinSimilarity float64
	BM25MinScore                  float64
	// RecallBudgetLow, Mid and High are how many candidates each budget level
	// lets the pipeline consider when the fixed function is in force.
	RecallBudgetLow  int
	RecallBudgetMid  int
	RecallBudgetHigh int
	// RecallBudgetFunction selects how a budget level maps to its candidate
	// bound: RecallBudgetFixed returns the three levels above,
	// RecallBudgetAdaptive scales the request's token budget by the level's
	// ratio and clamps the result. An absent or unrecognised value is the
	// fixed function, which is the reference's default.
	RecallBudgetFunction string
	// RecallBudgetAdaptiveLow, Mid and High are the adaptive function's ratios
	// of the request's token budget, and RecallBudgetMin and
	// RecallBudgetMax clamp its result. A non-positive bound is unset.
	RecallBudgetAdaptiveLow  float64
	RecallBudgetAdaptiveMid  float64
	RecallBudgetAdaptiveHigh float64
	RecallBudgetMin          int
	RecallBudgetMax          int
	// RerankerMaxCandidates is the cross-encoder candidate cap: the fused
	// candidate pool is trimmed to it before scoring, so candidates past the cap
	// are dropped rather than carried on their fused scores (the reference's
	// reranker_max_candidates, engine/memory_engine.py:10428-10431). Zero means
	// unset, which leaves the pool alone — the state of a store built without
	// the resolved tuning.
	RerankerMaxCandidates int
	// RerankerMaxCandidatesLow, Mid and High are the per-budget-level overrides
	// of RerankerMaxCandidates. Zero falls back to the flat cap, which is how
	// the reference resolves them (engine/memory_engine.py:1715-1730).
	RerankerMaxCandidatesLow  int
	RerankerMaxCandidatesMid  int
	RerankerMaxCandidatesHigh int
	// DiagnosticPhases turns the per-phase recall accounting on: every sampled
	// recall then measures its phases and reports them, as one observation per
	// phase to the store's phase observer and as the `[phases]` log line.
	// PhaseSampleEvery reports only one recall in N, which is how a deployment
	// trades the accounting's cost against its resolution; a value below two
	// reports every recall.
	DiagnosticPhases bool
	PhaseSampleEvery int
	// ReflectMaxIterations bounds the reflection loop's retrieval turns, and
	// ReflectContextBudget the accumulated evidence after which it synthesises.
	ReflectMaxIterations int
	ReflectContextBudget int
	// EntityCandidateSimilarity is the pg_trgm floor at or above which a stored
	// entity is worth scoring against a supplied name,
	// EntityResolutionCandidates bounds how many are scored, and
	// EntityMergeMinSimilarity is the reference's second trigram gate: a
	// candidate below it is not merged even when the scorer would accept it.
	EntityCandidateSimilarity  float64
	EntityResolutionCandidates int
	EntityMergeMinSimilarity   float64
	// MaxQueryTokens truncates a recall query at ingress; MaxConcurrent is the
	// engine-level permit count for concurrent recalls, separate from admission;
	// ConnectionBudget bounds the connections one recall's arms may hold at once.
	MaxQueryTokens   int
	MaxConcurrent    int
	ConnectionBudget int
	// BM25SelectiveTerms selects the BM25 arm's terms by document frequency when
	// true, and takes the positional first-N cap when false.
	BM25SelectiveTerms bool
	// InternalRecallIncludeChunks, InternalRecallMaxFactTokens and
	// InternalRecallMaxChunkTokens are the internal recall's channel settings:
	// whether it returns raw chunks and the two token budgets.
	InternalRecallIncludeChunks  bool
	InternalRecallMaxFactTokens  int
	InternalRecallMaxChunkTokens int
	// ExtractCausalLinks turns causal-relation extraction on or off.
	ExtractCausalLinks bool
	// EntityIntrabatchMerge is the pg_trgm similarity at or above which two
	// names appearing in the same extraction batch are merged before scoring.
	EntityIntrabatchMerge float64
	// Consolidation carries the consolidation pass's process-level knobs.
	Consolidation ConsolidationTuning
	// ReflectPromptCacheEnabled is the reflect-scoped prompt-cache switch, and
	// ReflectMaxCompletionTokens the cap on the final synthesis call (zero
	// leaves the parameter unset).
	ReflectPromptCacheEnabled  bool
	ReflectMaxCompletionTokens int
	// ANNIterativeScan lets a vector scan resume in rounds until the arm's limit
	// is met, and ANNMaxScanTuples bounds how far it may go. Both are pgvector's
	// own settings.
	ANNIterativeScan bool
	ANNMaxScanTuples int
	// ObservationHistory and MentalModelHistory are the reference's switches for
	// the two history records; the bounds below cap how many entries one
	// observation or one mental model keeps.
	ObservationHistory    bool
	MentalModelHistory    bool
	ObservationHistoryMax int
	MentalModelHistoryMax int
	// BankInfoCacheMaxEntries bounds the per-process bank-row cache.
	BankInfoCacheMaxEntries int
}

// DefaultTuning is the reference's tuning: the values its own deployment
// resolves when nothing is configured.
func DefaultTuning() Tuning {
	return Tuning{
		GraphSeedMinSimilarity:        DefaultGraphSeedMinSimilarity,
		SemanticMinSimilarity:         DefaultSemanticMinSimilarity,
		TemporalSemanticMinSimilarity: DefaultTemporalSemanticMinSimilarity,
		BM25MinScore:                  DefaultBM25MinScore,
		SemanticLinkMinSimilarity:     DefaultSemanticLinkMinSimilarity,
		BM25MaxQueryTerms:             DefaultBM25MaxQueryTerms,
		LinkExpansionPerEntityLimit:   DefaultLinkExpansionPerEntityLimit,
		LinkExpansionTimeout:          DefaultLinkExpansionTimeout,
		RecallBudgetLow:               DefaultRecallBudgetLow,
		RecallBudgetMid:               DefaultRecallBudgetMid,
		RecallBudgetHigh:              DefaultRecallBudgetHigh,
		RecallBudgetFunction:          DefaultRecallBudgetFunction,
		RecallBudgetAdaptiveLow:       DefaultRecallBudgetAdaptiveLow,
		RecallBudgetAdaptiveMid:       DefaultRecallBudgetAdaptiveMid,
		RecallBudgetAdaptiveHigh:      DefaultRecallBudgetAdaptiveHigh,
		RecallBudgetMin:               DefaultRecallBudgetMin,
		RecallBudgetMax:               DefaultRecallBudgetMax,
		DiagnosticPhases:              true,
		PhaseSampleEvery:              1,
		ReflectMaxIterations:          DefaultReflectMaxIterations,
		ReflectContextBudget:          DefaultReflectContextBudget,
		EntityMergeMinSimilarity:      DefaultEntityMergeMinSimilarity,
		MaxQueryTokens:                DefaultRecallMaxQueryTokens,
		ConnectionBudget:              DefaultRecallConnectionBudget,
		MaxConcurrent:                 DefaultRecallMaxConcurrent,
		BM25SelectiveTerms:            true,
		InternalRecallIncludeChunks:   true,
		InternalRecallMaxFactTokens:   DefaultRecallMaxTokens,
		InternalRecallMaxChunkTokens:  DefaultRecallChunksMaxTokens,
		ExtractCausalLinks:            true,
		EntityIntrabatchMerge:         DefaultEntityIntrabatchMergeSimilarity,
		Consolidation:                 DefaultConsolidationTuning(),
		ReflectPromptCacheEnabled:     true,
		ANNIterativeScan:              true,
		ANNMaxScanTuples:              DefaultANNMaxScanTuples,
		EntityCandidateSimilarity:     EntityCandidateSimilarity,
		EntityResolutionCandidates:    DefaultEntityResolutionCandidates,
		ObservationHistory:            true,
		MentalModelHistory:            true,
		ObservationHistoryMax:         DefaultHistoryMaxEntries,
		MentalModelHistoryMax:         DefaultHistoryMaxEntries,
		BankInfoCacheMaxEntries:       DefaultBankInfoCacheMaxEntries,
	}
}

// The reference's shipped tuning defaults, in one place so the store, the
// configuration table and the tests cannot drift apart.
const (
	DefaultGraphSeedMinSimilarity = 0.3
	// DefaultSemanticMinSimilarity and DefaultTemporalSemanticMinSimilarity are
	// the per-arm floors a query with no explicit floor runs under.
	DefaultSemanticMinSimilarity         = 0.3
	DefaultTemporalSemanticMinSimilarity = 0.1
	// DefaultBM25MinScore is the keyword arm's floor; zero means no floor.
	DefaultBM25MinScore              = 0.0
	DefaultSemanticLinkMinSimilarity = 0.7
	DefaultBM25MaxQueryTerms         = 16
	DefaultRecallBudgetLow           = 100
	DefaultRecallBudgetMid           = 300
	DefaultRecallBudgetHigh          = 1000
	// RecallBudgetFixed and RecallBudgetAdaptive name the two budget
	// functions; DefaultRecallBudgetFunction is the reference's shipped one.
	RecallBudgetFixed           = "fixed"
	RecallBudgetAdaptive        = "adaptive"
	DefaultRecallBudgetFunction = RecallBudgetFixed
	// DefaultRecallBudgetAdaptiveLow, Mid and High are the adaptive function's
	// ratios of the request's token budget, and DefaultRecallBudgetMin and
	// DefaultRecallBudgetMax the bounds it clamps to.
	DefaultRecallBudgetAdaptiveLow  = 0.025
	DefaultRecallBudgetAdaptiveMid  = 0.075
	DefaultRecallBudgetAdaptiveHigh = 0.25
	DefaultRecallBudgetMin          = 20
	DefaultRecallBudgetMax          = 2000
	// DefaultRecallBudgetScaleTokens is the token budget the adaptive function
	// scales when a caller names none: the reference's own default response
	// budget for a recall, and the value its shipped fixed levels were derived
	// from.
	DefaultRecallBudgetScaleTokens = 4096
	DefaultReflectMaxIterations    = 10
	DefaultReflectContextBudget    = 100000
	// DefaultHistoryMaxEntries caps how many entries one observation's or one
	// mental model's history keeps.
	DefaultHistoryMaxEntries = 50
	// DefaultEntityResolutionCandidates bounds how many stored entities one
	// mention scores before the merge decision.
	DefaultEntityResolutionCandidates = 200
	// DefaultLinkExpansionPerEntityLimit caps one entity's graph-expansion
	// targets, and DefaultLinkExpansionTimeout bounds that statement.
	DefaultLinkExpansionPerEntityLimit = 200
	DefaultLinkExpansionTimeout        = 10 * time.Second
	// DefaultBankInfoCacheMaxEntries bounds the per-process bank-row cache.
	DefaultBankInfoCacheMaxEntries = 2048
	// DefaultANNMaxScanTuples bounds one resumed ANN scan.
	DefaultANNMaxScanTuples = 4000
	// DefaultEntityMergeMinSimilarity is the reference's second trigram gate on
	// an entity candidate, and DefaultEntityIntrabatchMergeSimilarity the floor
	// at which two names in one batch are treated as the same entity.
	DefaultEntityMergeMinSimilarity        = 0.3
	DefaultEntityIntrabatchMergeSimilarity = 0.5
	// DefaultRecallMaxQueryTokens bounds a recall query's length, and
	// DefaultRecallMaxConcurrent the recalls one process runs at once.
	DefaultRecallMaxQueryTokens = 500
	// DefaultRecallConnectionBudget bounds the connections one recall's arms may
	// hold at once.
	DefaultRecallConnectionBudget = 4
	DefaultRecallMaxConcurrent    = 32
	// DefaultRecallMaxTokens and DefaultRecallChunksMaxTokens are the internal
	// recall's two channel budgets.
	DefaultRecallMaxTokens       = 2048
	DefaultRecallChunksMaxTokens = 1000
	// DefaultConsolidation* are the consolidation pass's reference defaults.
	DefaultConsolidationMemoryBatchSize = 50
	DefaultConsolidationDedupThreshold  = 0.97
	DefaultConsolidationMaxAttempts     = 3
	DefaultConsolidationMaxTokens       = 512
	DefaultConsolidationRecallBudget    = "low"
)

// ConsolidationTuning is the consolidation pass's process-level tuning: the
// values the reference resolves from the environment and layers under a bank's
// own consolidation settings.
type ConsolidationTuning struct {
	// MemoryBatchSize is how many memories one load batch carries.
	MemoryBatchSize int
	// DedupThreshold is the cosine similarity at or above which an existing
	// observation of the same scope is treated as a duplicate and folded
	// instead of creating a second row; a value of 1 disables dedup, as the
	// reference's own switch does.
	DedupThreshold float64
	// MaxCompletionTokens caps the model call; zero leaves it unset.
	MaxCompletionTokens int
	// MaxAttempts is the per-operation attempt budget.
	MaxAttempts int
	// MaxTokens is the recall token budget the pass searches related
	// observations with, and RecallBudget the budget level it asks for.
	MaxTokens    int
	RecallBudget string
}

// DefaultConsolidationTuning is the reference's consolidation defaults.
func DefaultConsolidationTuning() ConsolidationTuning {
	return ConsolidationTuning{
		MemoryBatchSize: DefaultConsolidationMemoryBatchSize,
		DedupThreshold:  DefaultConsolidationDedupThreshold,
		MaxAttempts:     DefaultConsolidationMaxAttempts,
		MaxTokens:       DefaultConsolidationMaxTokens,
		RecallBudget:    DefaultConsolidationRecallBudget,
	}
}

// newRecallGate builds the engine-level recall permit gate. A non-positive
// bound means unbounded, which a nil gate expresses.
func newRecallGate(maxConcurrent int) chan struct{} {
	if maxConcurrent <= 0 {
		return nil
	}
	return make(chan struct{}, maxConcurrent)
}

// acquirePermit takes one permit from a gate, waiting until one is free or the
// context ends. It returns the release function and whether the caller may
// proceed. A nil gate is unbounded: the release is a no-op.
func acquirePermit(ctx context.Context, gate chan struct{}) (func(), bool) {
	if gate == nil {
		return func() {}, true
	}
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, true
	case <-ctx.Done():
		return nil, false
	}
}

// acquireRecall takes a permit from this store's gate, so no more than the
// configured number of recalls run at once.
func (s *PostgresStore) acquireRecall(ctx context.Context) (func(), bool) {
	return acquirePermit(ctx, s.recallGate)
}

// acquireRecall is the in-process backend's equivalent.
func (s *MemoryStore) acquireRecall(ctx context.Context) (func(), bool) {
	return acquirePermit(ctx, s.recallGate)
}

// withDefaultFloors applies the deployment's per-arm score floors to a query
// the caller set none for. An arm's own floor always wins, because a caller who
// named one stated what they mean; the deployment's value is the default the
// reference applies to an unfloored query.
func (t Tuning) withDefaultFloors(q RecallQuery) RecallQuery {
	floors := map[string]float64{
		ArmSemantic: t.SemanticMinSimilarity,
		ArmTemporal: t.TemporalSemanticMinSimilarity,
		ArmKeyword:  t.BM25MinScore,
	}
	// The map is copied before it is extended: the caller's query is theirs, and
	// a floor this layer added must not appear in it.
	extended := make(map[string]float64, len(q.MinScores)+len(floors))
	for arm, floor := range q.MinScores {
		extended[arm] = floor
	}
	for arm, floor := range floors {
		if floor <= 0 {
			continue
		}
		if _, set := q.MinScores[arm]; set {
			continue
		}
		extended[arm] = floor
	}
	if len(extended) > 0 {
		q.MinScores = extended
	}
	return q
}

// BudgetLimit maps a budget level to the candidate bound it allows under the
// fixed function. An absent or unrecognised level falls back to the middle
// level, which is what an omitted budget means.
func (t Tuning) BudgetLimit(budget string) int {
	switch budget {
	case BudgetLow:
		return t.RecallBudgetLow
	case BudgetHigh:
		return t.RecallBudgetHigh
	default:
		return t.RecallBudgetMid
	}
}

// BudgetLimitForQuery resolves the candidate bound a recall runs under: the
// level's fixed bound when the fixed function is in force, and otherwise the
// level's ratio of the request's own token budget, clamped to the configured
// bounds. A request that names no token budget scales the reference's default
// recall budget, so the adaptive function still has a scale to work from.
//
// The ratio scales the token budget rather than the candidate count: the bound
// exists to bound retrieval work, and the reference derives it from how much
// text the caller asked to receive.
func (t Tuning) BudgetLimitForQuery(q RecallQuery) int {
	if !strings.EqualFold(strings.TrimSpace(t.RecallBudgetFunction), RecallBudgetAdaptive) {
		return t.BudgetLimit(q.Budget)
	}
	scale := q.MaxTokens
	if scale <= 0 {
		scale = DefaultRecallBudgetScaleTokens
	}
	raw := int(math.Round(float64(scale) * t.budgetRatio(q.Budget)))
	// A bound of zero or below is unset and clamps nothing, so a store built
	// without the resolved tuning keeps every ratio's own result.
	if t.RecallBudgetMin > 0 && raw < t.RecallBudgetMin {
		return t.RecallBudgetMin
	}
	if t.RecallBudgetMax > 0 && raw > t.RecallBudgetMax {
		return t.RecallBudgetMax
	}
	return raw
}

// budgetRatio returns the adaptive function's ratio for a level. An absent or
// unrecognised level falls back to the middle ratio, matching BudgetLimit's
// own fallback.
func (t Tuning) budgetRatio(budget string) float64 {
	switch budget {
	case BudgetLow:
		return t.RecallBudgetAdaptiveLow
	case BudgetHigh:
		return t.RecallBudgetAdaptiveHigh
	default:
		return t.RecallBudgetAdaptiveMid
	}
}

// RerankerMaxCandidatesFor resolves the cross-encoder candidate cap for a
// budget level: the level's override when it is set (> 0), otherwise the flat
// cap. An absent or unrecognised level falls back to the middle level, which is
// what an omitted budget means — the reference resolves the cap per recall the
// same way (engine/memory_engine.py:9532 with :1715-1730).
func (t Tuning) RerankerMaxCandidatesFor(budget string) int {
	switch budget {
	case BudgetLow:
		if t.RerankerMaxCandidatesLow > 0 {
			return t.RerankerMaxCandidatesLow
		}
	case BudgetHigh:
		if t.RerankerMaxCandidatesHigh > 0 {
			return t.RerankerMaxCandidatesHigh
		}
	default:
		if t.RerankerMaxCandidatesMid > 0 {
			return t.RerankerMaxCandidatesMid
		}
	}
	return t.RerankerMaxCandidates
}
