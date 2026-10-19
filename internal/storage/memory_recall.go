package storage

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

// RecallQueryTokenLimit reports the process-level cap on a recall query's
// length in tokens, so the HTTP ingress can bound the query before it reaches
// the embedder. A non-positive value means the cap is off.
func (s *MemoryStore) RecallQueryTokenLimit() int { return s.tuning.MaxQueryTokens }

// Recall runs the multi-arm retrieval and ranking pipeline for the in-process
// store. It returns a single ranked list whose order is descending by combined
// relevance, plus any requested side channels.
func (s *MemoryStore) Recall(ctx context.Context, q RecallQuery) (RecallResult, error) {
	// Recalls are bounded at the engine level, separately from admission
	// control: more than the configured number in flight wait for a permit
	// rather than all running at once.
	release, ok := s.acquireRecall(ctx)
	if !ok {
		return RecallResult{}, ctx.Err()
	}
	defer release()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return RecallResult{}, s.readErr
	}
	q = s.tuning.withDefaultFloors(q)
	// The query is bounded at ingress, before any arm reads it: a degenerate
	// query would otherwise fan out into an unbounded OR tsquery. Truncation,
	// not rejection, so an over-long query still answers.
	q.Query = TruncateQueryToTokens(q.Query, s.tuning.MaxQueryTokens)
	// The diagnostic accounting is off by default in a store built without the
	// resolved tuning, and skipped for every recall a sampling rate passes
	// over: an unsampled recall measures nothing.
	phases := s.beginRecallPhases(q.BankID)
	defer phases.finish()
	stop := phases.measure("bank_lookup", false)
	if _, ok := s.banks[q.BankID]; !ok {
		stop()
		return RecallResult{}, ErrBankNotFound
	}
	stop()
	// Fuzzy tag leaves resolve against the bank's own vocabulary once, here,
	// before any arm runs: every arm then filters on literal tags, so no caller
	// can reach a divergent per-row reading by choosing an arm. The resolution
	// caps are enforced during resolution, so an over-large scope fails fast
	// with an error the caller maps to a 422 rather than grinding.
	if usesFuzzyTagResolution(q.TagGroups) {
		stop = phases.measure("fuzzy_tags", false)
		resolved, err := ResolveRecallTagScope(q, s.tagVocabulary(q.BankID))
		stop()
		if err != nil {
			return RecallResult{}, err
		}
		q = resolved
	}
	types := resolvedTypes(q.Types)
	arms := activeArms(q)
	term := strings.TrimSpace(q.Query)
	// The budget level bounds the retrieval work, so it caps how many
	// candidates survive ranking unless the caller named an explicit limit.
	limit := q.Limit
	if limit <= 0 {
		limit = s.tuning.BudgetLimitForQuery(q)
	}

	// Per-arm best-first candidate lists. The arms are independent, so they run
	// concurrently, bounded by the recall's connection budget; their results are
	// then merged in arm order, so the fused pool does not depend on which arm
	// finished first.
	stop = phases.measure("parallel_retrieval", false)
	armJobs := make([]recallArmJob, 0, len(arms))
	for _, arm := range arms {
		armJobs = append(armJobs, recallArmJob{name: arm, run: func(context.Context) ([]recallHit, error) {
			return s.runArm(arm, q, types, term), nil
		}})
	}
	armOutcomes, err := runRecallArms(ctx, newRecallArmFanOut(s.tuning.ConnectionBudget), phases, armJobs)
	stop()
	if err != nil {
		return RecallResult{}, err
	}
	armHits := make(map[string][]recallHit, len(arms))
	for index, arm := range arms {
		armHits[arm] = armOutcomes[index].hits
	}

	armTraces := make([]recallArmTrace, 0, len(arms))
	for _, arm := range arms {
		armTraces = append(armTraces, recallArmTrace{arm: arm, hits: prepareArm(q, arm, armHits[arm])})
	}

	// Merge candidates across arms, keeping the first rank observed per fact.
	// Each arm is capped to its best results first, so one over-expanding
	// backend cannot crowd the others out of the merged pool.
	candidates := map[string]*recallCandidate{}
	for _, trace := range armTraces {
		for rank, hit := range trace.hits {
			cand := candidates[hit.match.ID]
			if cand == nil {
				m := hit.match
				cand = &recallCandidate{
					match:     m,
					armRanks:  map[string]int{},
					armScores: map[string]float64{},
				}
				candidates[hit.match.ID] = cand
			}
			if _, seen := cand.armRanks[trace.arm]; !seen {
				cand.armRanks[trace.arm] = rank + 1
				cand.armScores[trace.arm] = hit.score
			}
		}
	}

	// Fusion ordering (reciprocal rank fusion) then combined scoring.
	stop = phases.measure("score", false)
	ranked := scoreCandidates(ctx, candidates, arms, q)
	ranked = orderByFinal(ranked)
	ranked = filterFloors(ranked, q.MinScores)
	ranked = preferObservations(ranked, q)
	ranked = capCandidates(ranked, limit)
	stop()

	result := RecallResult{Matches: make([]RecallMatch, 0, len(ranked))}
	for _, cand := range ranked {
		result.Matches = append(result.Matches, cand.toMatch())
	}
	stop = phases.measure("hydrate_results", false)
	s.enrichRecallAttachments(q.BankID, result.Matches)
	stop()
	stop = phases.measure("side_channels", false)
	s.attachSideChannels(&result, q, types)
	stop()
	// The budgets are applied after the channels are derived, so a channel is
	// capped by its own budget rather than by the fact budget. The caller-set
	// values only ever narrow: zero means unbounded, which is what every caller
	// that names no budget gets.
	stop = phases.measure("token_filter", false)
	result.Matches = capMatchesToTokens(result.Matches, q.MaxTokens)
	result.ChunkEntries = capChunksToTokens(result.ChunkEntries, q.MaxChunkTokens)
	stop()
	// Entities are resolved after the fact budget has selected the returned
	// matches: the entity channel is a view of those matches, so a fact the
	// budget dropped must not leave its entities behind.
	if q.IncludeEntities {
		stop = phases.measure("entity_build", false)
		s.attachRecallEntities(q, &result)
		stop()
	}
	stop = phases.measure("trace", false)
	result.Trace = buildRecallTrace(candidates, ranked, armTraces, q, limit)
	stop()
	return result, nil
}

// capCandidates truncates the ranked list to the budget's candidate bound,
// keeping the best-scoring entries. The list is already in descending order, so
// the tail is what a tighter budget drops.
func capCandidates(ranked []*recallCandidate, limit int) []*recallCandidate {
	if limit <= 0 || len(ranked) <= limit {
		return ranked
	}
	return ranked[:limit]
}

// buildRecallTrace records what the pipeline looked at, so the trace route can
// report it without re-deriving the counts from the ranked list. It also
// assembles the reference-shaped retrieval detail from the arm lists the
// pipeline fanned out to.
func buildRecallTrace(candidates map[string]*recallCandidate, ranked []*recallCandidate, armTraces []recallArmTrace, q RecallQuery, limit int) RecallTrace {
	// Every arm a candidate was surfaced by is counted, so the trace reports
	// which retrieval paths actually contributed rather than a fixed subset.
	stageCounts := map[string]int{}
	for _, cand := range ranked {
		for stage := range cand.armRanks {
			stageCounts[stage]++
		}
	}
	return RecallTrace{
		Budget:          limit,
		BudgetUsed:      len(ranked),
		CandidatesFound: len(candidates),
		StageCounts:     stageCounts,
		Detail:          buildRecallTraceDetail(candidates, ranked, armTraces, q, limit),
	}
}

// resolvedTypes returns the fact-type selection, defaulting to every supported
// type when the selection is empty.
func resolvedTypes(types []string) []string {
	if len(types) == 0 {
		out := make([]string, 0, len(FinalMemoryUnitTypes()))
		for _, t := range FinalMemoryUnitTypes() {
			out = append(out, string(t))
		}
		return out
	}
	return types
}

// activeArms returns the retrieval arms to run, honoring bank-disabled arms. An
// arm disabled by configuration is omitted entirely rather than having its rows
// filtered away.
func activeArms(q RecallQuery) []string {
	disabled := map[string]bool{}
	for _, arm := range q.DisabledArms {
		disabled[arm] = true
	}
	out := make([]string, 0, len(RecallArms()))
	for _, arm := range RecallArms() {
		if disabled[arm] {
			continue
		}
		out = append(out, arm)
	}
	return out
}

// recallHit is one arm result: a matched memory and the arm's score for it.
type recallHit struct {
	match RecallMatch
	score float64
}

// recallCandidate is a fused candidate accumulating per-arm evidence.
type recallCandidate struct {
	match     RecallMatch
	armRanks  map[string]int
	armScores map[string]float64
	// rrfScore is the reciprocal-rank-fusion total before any boost, and
	// components is the breakdown the trace reports.
	rrfScore   float64
	components map[string]float64
}

// rrfConstant is the reciprocal-rank-fusion smoothing constant.
const rrfConstant = 60.0

// boostWeights is the tuned magnitude pair for one priority level.
//
// The two fields deliberately live on different scales: rankDivisor divides a
// boosted arm's rank before its RRF contribution is computed, while additive is
// added directly to the post-rerank weight.
type boostWeights struct {
	rankDivisor float64
	additive    float64
}

// boostLevels maps a priority level to its per-stage weights. The keys are the
// user-facing contract; the values are the tuned pair the reference uses.
var boostLevels = map[string]boostWeights{
	"low":    {rankDivisor: 2, additive: 0.05},
	"medium": {rankDivisor: 4, additive: 0.2},
	"high":   {rankDivisor: 8, additive: 0.5},
}

// flatStage2Arms are the arms whose post-rerank bump does not decay with rank.
// The temporal arm is ranked by date proximity to the query window rather than
// by relevance, so decaying by that rank would hand the bump to the memories
// nearest the window midpoint instead of the ones the reranker rated best.
var flatStage2Arms = map[string]bool{"temporal": true}

// stage1Boost promotes a candidate's boosted arms in rank space and returns the
// score to sort by.
//
// The boosted arm's RRF contribution is recomputed as if the candidate had
// placed rank/divisor instead of rank, expressed as a delta so the unboosted
// contributions stay untouched. Boosting the rank rather than the score is what
// keeps the displacement independent of the RRF constant and the pool size: a
// boosted candidate at arm-rank r beats an unboosted one at arm-rank s exactly
// when r < divisor*s, whereas multiplying the score would let the constant term
// dominate at the head of the ranking.
func stage1Boost(cand *recallCandidate, fused float64, boosts map[string]string) float64 {
	if len(boosts) == 0 {
		return fused
	}
	delta := 0.0
	for arm, level := range boosts {
		rank, ok := cand.armRanks[arm]
		if !ok {
			continue
		}
		weights, known := boostLevels[strings.ToLower(strings.TrimSpace(level))]
		if !known {
			continue
		}
		delta += 1/(rrfConstant+float64(rank)/weights.rankDivisor) - 1/(rrfConstant+float64(rank))
	}
	return fused + delta
}

// stage2Boost returns the post-rerank bump for a candidate.
//
// Each boosted arm the candidate was surfaced by contributes the level's
// additive at rank 1, decaying toward zero as its rank in that arm worsens;
// a flat arm contributes the full additive at every rank. Matched arms are
// summed, so two rank-1 high hits add up. An arm the candidate was not
// surfaced by contributes nothing.
func stage2Boost(cand *recallCandidate, boosts map[string]string) float64 {
	if len(boosts) == 0 {
		return 0
	}
	total := 0.0
	for arm, level := range boosts {
		rank, ok := cand.armRanks[arm]
		if !ok {
			continue
		}
		weights, known := boostLevels[strings.ToLower(strings.TrimSpace(level))]
		if !known {
			continue
		}
		if flatStage2Arms[arm] {
			total += weights.additive
			continue
		}
		total += weights.additive * weights.rankDivisor / (weights.rankDivisor + float64(rank) - 1)
	}
	return total
}

// scoreCandidates fuses per-arm ranks and applies combined scoring. A boost
// configured for an arm promotes that arm's candidates in rank space before
// fusion and gives them a rank-decaying post-rerank bump; unboosted arms keep
// the head of their own ranking.
func scoreCandidates(ctx context.Context, candidates map[string]*recallCandidate, arms []string, q RecallQuery) []*recallCandidate {
	list := make([]*recallCandidate, 0, len(candidates))
	for _, cand := range candidates {
		list = append(list, cand)
	}
	// Deterministic base order so equal scores resolve by id.
	sort.SliceStable(list, func(i, j int) bool { return list[i].match.ID < list[j].match.ID })

	// The cross-encoder scores the fused pool once, in the deterministic order
	// above, so a score can be paired back to its candidate by position. A
	// failure leaves every candidate on the best-per-arm fallback. A
	// rank-position reranker's output carries no relevance of its own, so its
	// base is seeded from the fused ordering instead (see passthroughRerankSeed).
	reranked := rerankCandidates(ctx, list, q)
	// A pruning reranker judges relevance, marking a candidate it would remove
	// with a score of exactly 0.0. Acting on that verdict here — where the
	// candidates live — is what shrinks the result; the reranker itself only
	// returns one score per pair and never sees them. A candidate the reranker
	// left unscored carries NaN and is never pruned, because NaN means "keep
	// the fused score", not "irrelevant".
	if reranked != nil && q.RerankPrunesCandidates {
		keptList := list[:0]
		keptScores := reranked[:0]
		for i, cand := range list {
			if !(reranked[i] == 0 && !math.IsNaN(reranked[i])) {
				keptList = append(keptList, cand)
				keptScores = append(keptScores, reranked[i])
			}
		}
		list = keptList
		reranked = keptScores
	}
	var seed []float64
	if reranked != nil && q.RerankSeedFromFused {
		seed = passthroughRerankSeed(list, arms)
	}

	for index, cand := range list {
		fused := 0.0
		for _, arm := range arms {
			if rank, ok := cand.armRanks[arm]; ok {
				fused += 1 / (rrfConstant + float64(rank))
			}
		}
		cand.rrfScore = fused
		fused = stage1Boost(cand, fused, q.StrategyBoosts)
		// Relevance: when the cross-encoder ran, its normalized score is the
		// base and the reciprocal-rank fusion is dropped from it — the
		// reference excludes RRF from the post-rerank score. Without a
		// reranker the base is the fused score scaled by the best arm score
		// (neutral when no arm surfaced a positive one). A candidate the
		// reranker did not score — a partial result or one past the rerank
		// depth — keeps that same fused base, which is what "unscored
		// candidates keep their fused order" means.
		base := fused * neutralArmScore(cand)
		scoredByReranker := reranked != nil && !math.IsNaN(reranked[index])
		if scoredByReranker {
			if seed != nil {
				base = seed[index]
			} else {
				base = normalizeRerankScore(reranked[index], reranked)
			}
		}
		// The combined boost multiplies the base (recency, temporal proximity,
		// proof strength).
		boost := combinedBoost(cand, q)
		final := base * boost
		if boost == 0 {
			final = base
		}
		// The strategy boost is a post-rerank add: an absolute bump on the
		// final weight, not scaled by the fused score.
		final += stage2Boost(cand, q.StrategyBoosts)
		cand.match.Final = final
		cand.match.ArmRanks = cloneIntMap(cand.armRanks)
		cand.match.Scores = candidateScores(cand, q)
		// The score breakdown the trace reports. It always carries the RRF,
		// temporal, recency, proof and combined components; the cross-encoder
		// pair is added when a reranker scored this candidate.
		recency, temporal, proof := combinedSignals(cand, q)
		components := map[string]float64{
			"rrf_score":      fused,
			"temporal":       temporal,
			"recency":        recency,
			"proof_norm":     proof,
			"combined_score": final,
		}
		if scoredByReranker {
			components["cross_encoder_score"] = reranked[index]
			components["cross_encoder_score_normalized"] = base
		}
		cand.components = components
		// A rank-position reranker's value is not a relevance score, so it is
		// not published: the top candidate would read 1.0 on every recall
		// however weak it is, and a consumer comparing it to a floor would be
		// misled.
		if reranked != nil && !q.RerankRankPosition && !math.IsNaN(reranked[index]) {
			cand.match.Scores["reranker"] = base
		}
	}
	// The RRF component is normalized across the batch so the trace reports a
	// comparable value rather than the raw reciprocal-rank sum.
	normalizeRRFComponent(list)
	// Interleave fusion is an explicit caller choice where the position *is*
	// the order: each arm is guaranteed a slot, so the score is strictly
	// decreasing by interleave position and the sort downstream reproduces it.
	if usesInterleave(q) {
		ordered := interleaveFusion(list, arms)
		total := len(ordered)
		for position, cand := range ordered {
			cand.match.Final = float64(total - position)
		}
		return ordered
	}
	return list
}

// normalizeRRFComponent writes the batch-normalized RRF score into every
// candidate's component map, so the trace reports a comparable value rather
// than the raw reciprocal-rank sum.
func normalizeRRFComponent(list []*recallCandidate) {
	low, high := math.Inf(1), math.Inf(-1)
	for _, cand := range list {
		if cand.components == nil {
			continue
		}
		value := cand.components["rrf_score"]
		if value < low {
			low = value
		}
		if value > high {
			high = value
		}
	}
	for _, cand := range list {
		if cand.components == nil {
			continue
		}
		value := cand.components["rrf_score"]
		switch {
		case math.IsInf(low, 1):
			cand.components["rrf_normalized"] = 0
		case high <= low:
			cand.components["rrf_normalized"] = 1
		default:
			cand.components["rrf_normalized"] = (value - low) / (high - low)
		}
	}
}

// neutralArmScore returns the best arm score, or 1 when no arm surfaced a
// positive score, so the fused weight still governs a candidate no arm scored.
func neutralArmScore(cand *recallCandidate) float64 {
	score := bestArmScore(cand)
	if score <= 0 {
		return 1
	}
	return score
}

// rerankCandidates scores the fused pool through the cross-encoder boundary,
// returning one score per candidate in list order. It returns nil when no
// reranker is configured, the pool is empty, or the call failed outright — all
// of which leave the caller on its fused fallback.
//
// A candidate past the rerank depth, and any candidate a partial reranker left
// unscored, is marked NaN so the caller keeps its fused score rather than
// pairing it with a score it never earned.
func rerankCandidates(ctx context.Context, list []*recallCandidate, q RecallQuery) []float64 {
	if q.Rerank == nil || len(list) == 0 {
		return nil
	}
	depth := len(list)
	if q.RerankDepth > 0 && depth > q.RerankDepth {
		depth = q.RerankDepth
	}
	texts := make([]string, 0, depth)
	for _, cand := range list[:depth] {
		texts = append(texts, cand.match.Text)
	}
	scores, err := q.Rerank(ctx, q.Query, texts)
	if err != nil {
		// A partial result is the point of the error: the reranker scored what
		// it could before failing or timing out, and the remainder should keep
		// the fused order rather than the whole recall falling back.
		var partial *PartialRerankError
		if !errors.As(err, &partial) || len(partial.Scores) != len(texts) {
			return nil
		}
		scores = partial.Scores
	}
	if len(scores) != len(texts) {
		return nil
	}
	// Restore one score per candidate so the slice indexes by position whether
	// or not the pool was truncated: candidates the reranker never saw carry
	// NaN, which the caller reads as "keep the fused score".
	if len(scores) < len(list) {
		padded := make([]float64, len(list))
		copy(padded, scores)
		for i := depth; i < len(list); i++ {
			padded[i] = math.NaN()
		}
		scores = padded
	}
	return scores
}

// passthroughRerankSeed derives the base score from the fused ordering for a
// rank-position reranker (RecallQuery.RerankRankPosition).
//
// A rank-position reranker returns scores that echo candidate position rather
// than measuring relevance, so min-max normalising them adds no information and
// can even invert the fused order. Seeding rank → [0.1, 1.0] from the
// descending fused order instead preserves what fusion already computed and
// leaves the recency / temporal / proof boosts room to separate neighbours,
// which is exactly the reference's passthrough seeding. The condition that
// selects it is RerankRankPosition together with a non-nil reranker result;
// every other case keeps the normalizeRerankScore path.
//
// The returned slice is indexed by the candidate's position in list.
func passthroughRerankSeed(list []*recallCandidate, arms []string) []float64 {
	fused := make([]float64, len(list))
	for i, cand := range list {
		total := 0.0
		for _, arm := range arms {
			if rank, ok := cand.armRanks[arm]; ok {
				total += 1 / (rrfConstant + float64(rank))
			}
		}
		fused[i] = total
	}
	order := make([]int, len(list))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ia, ib := order[a], order[b]
		if fused[ia] != fused[ib] {
			return fused[ia] > fused[ib]
		}
		return list[ia].match.ID < list[ib].match.ID
	})
	seed := make([]float64, len(list))
	denom := float64(len(list) - 1)
	if denom < 1 {
		denom = 1
	}
	for rank, index := range order {
		seed[index] = 1 - 0.9*float64(rank)/denom
	}
	return seed
}

// normalizeRerankScore maps a reranked score onto [0, 1]. A batch already in
// [0, 1] is passed through, preserving the provider's calibrated confidence; a
// batch that looks like logits is squashed through a logistic; anything else is
// min-max normalized over the batch. A batch with no range yields the neutral 1
// so fusion ordering stays intact.
//
// NaN entries mark candidates the reranker never scored (a partial result or a
// candidate past the rerank depth). They are excluded from the batch range: a
// partial result still normalizes against the scores that were produced, and an
// unscored candidate is left to the caller's fused fallback.
func normalizeRerankScore(score float64, scores []float64) float64 {
	if scores == nil || math.IsNaN(score) {
		return 0
	}
	low, high := math.Inf(1), math.Inf(-1)
	for _, value := range scores {
		if math.IsNaN(value) {
			continue
		}
		if value < low {
			low = value
		}
		if value > high {
			high = value
		}
	}
	if math.IsInf(low, 1) {
		// No finite score to normalize against.
		return 0
	}
	// Provider scores that already sit in [0, 1] are calibrated confidence and
	// are returned unchanged.
	if low >= 0 && high <= 1 {
		if score <= 0 {
			return 0.01
		}
		return score
	}
	// A batch outside [0, 1] is treated as raw logits and squashed. The sigmoid
	// keeps a top candidate high and a low one low, rather than stretching the
	// batch's own range to [0, 1] regardless of absolute confidence.
	if low >= -50 && high <= 50 {
		return logistic(score)
	}
	if high <= low {
		return 1
	}
	normalized := (score - low) / (high - low)
	return 0.01 + 0.99*normalized
}

// logistic is the standard sigmoid, mapping a logit onto (0, 1).
func logistic(x float64) float64 {
	return 1 / (1 + math.Exp(-x))
}

// bestArmScore returns the strongest per-arm score for a candidate.
func bestArmScore(cand *recallCandidate) float64 {
	best := 0.0
	for _, score := range cand.armScores {
		if score > best {
			best = score
		}
	}
	return best
}

// Boost tunings, matching the reference implementation. Each alpha is the
// maximum *relative* adjustment the signal can make, so a boost factor stays in
// [1-alpha/2, 1+alpha/2] and never overpowers the base relevance score it
// modulates.
const (
	recencyAlpha    = 0.2
	temporalAlpha   = 0.2
	proofCountAlpha = 0.1
)

// confidenceBoost turns a signal in [0, 1] into its multiplicative factor:
// 1.0 at the neutral 0.5 and up to 1±alpha/2 at the extremes.
func confidenceBoost(signal, alpha float64) float64 { return 1 + alpha*(signal-0.5) }

// combinedBoost multiplies bounded recency, temporal-proximity and
// proof-count factors. Each factor is neutral (1) when its signal is absent.
//
// The strategy boost is deliberately not here: it is a post-rerank add on a
// different scale, so it is applied by stage2Boost to the final weight instead
// of multiplying into it.
func combinedBoost(cand *recallCandidate, q RecallQuery) float64 {
	if len(cand.armScores) == 0 {
		return 1
	}
	recency, temporal, proof := combinedSignals(cand, q)
	return confidenceBoost(recency, recencyAlpha) *
		confidenceBoost(temporal, temporalAlpha) *
		confidenceBoost(proof, proofCountAlpha)
}

// combinedSignals returns the three bounded signals the combined boost folds
// in: the recency of the match, its proximity to a requested window, and the
// strength of its evidence. Each is in [0, 1] with 0.5 neutral.
func combinedSignals(cand *recallCandidate, q RecallQuery) (recency, temporal, proof float64) {
	// Recency is graded by the match's age rather than a flat present/absent
	// bump, so a fresh memory ranks above a stale one when relevance ties. The
	// decay shape is the deployment's choice.
	anchor := time.Now().UTC()
	if q.QueryTimestamp != nil {
		anchor = *q.QueryTimestamp
	}
	recency = RecencyForUnit(cand.match.OccurredStart, cand.match.OccurredEnd, cand.match.MentionedAt, anchor, q.Recency)
	// Temporal proximity is meaningful only for a query that named a window.
	// It grades toward the window's midpoint rather than answering
	// in/out: a unit dated at the centre scores 1, one at either edge scores 0,
	// and one with no window (or no date) is neutral 0.5.
	temporal = temporalProximity(q.Temporal, cand.match.OccurredStart, cand.match.OccurredEnd, cand.match.MentionedAt)
	// Proof count measures evidence strength, which only an observation has.
	// The logarithmic curve keeps a large count from dominating: 1 proof is
	// neutral and the signal saturates well before the count is huge.
	proof = 0.5
	if count := len(cand.match.SourceMemoryIDs); count >= 1 {
		proof = math.Max(0, math.Min(1, 0.5+math.Log(float64(count))/10))
	}
	return recency, temporal, proof
}

// temporalProximity grades a unit's closeness to the requested window onto
// [0, 1] with a neutral 0.5: a unit dated at the window's midpoint scores 1, one
// at either edge (or beyond) scores 0, and a unit with no usable date, or a
// request with no window, stays neutral.
//
// This is the graded form of the reference's temporal-proximity signal. The
// earlier in/out reading threw away the ordering the window already encodes:
// every in-window unit tied at 1, so a unit at the centre could not outrank one
// at the fringe even though the caller's window says it is nearer.
func temporalProximity(window *TemporalWindow, occurredStart, occurredEnd, mentionedAt *time.Time) float64 {
	if window == nil {
		return 0.5
	}
	best := unitTemporalPoint(occurredStart, occurredEnd, mentionedAt)
	if best == nil {
		return 0.5
	}
	// A window that does not name both bounds cannot define a midpoint or a
	// span, so it degrades to the containment reading. The request boundary
	// always supplies a pair; this is defensive.
	if !window.HasStart || !window.HasEnd {
		if window.Contains(occurredStart, occurredEnd) {
			return 1
		}
		return 0.5
	}
	span := window.End.Sub(window.Start)
	if span <= 0 {
		// An instant window: any dated unit is exactly on it, which is the
		// reference's reading of a zero-length window.
		return 1
	}
	mid := window.Start.Add(span / 2)
	daysFromMid := math.Abs(best.Sub(mid).Hours() / 24)
	halfSpanDays := span.Hours() / 24 / 2
	return 1 - math.Min(daysFromMid/halfSpanDays, 1)
}

// unitTemporalPoint reduces a unit's dates to the single instant the window
// proximity grades. A genuine span is read from its midpoint; a unit carrying
// only one date (or only a mention) uses that. Nil when the unit has no date.
func unitTemporalPoint(start, end, mentioned *time.Time) *time.Time {
	if start != nil && end != nil {
		mid := start.Add(end.Sub(*start) / 2)
		return &mid
	}
	if start != nil {
		return start
	}
	if end != nil {
		return end
	}
	return mentioned
}

// candidateScores builds the per-stage score object as a stage-keyed map. A
// stage that did not surface the candidate reports no value.
//
// Only the stages the reference's recall envelope reports appear here. The
// temporal signal is deliberately absent: the reference grades the unit's
// proximity to a requested window and folds it into the combined weight, but
// reports only final/reranker/semantic/keyword, so a temporal key would be a
// surface of our own invention.
func candidateScores(cand *recallCandidate, q RecallQuery) map[string]float64 {
	scores := map[string]float64{}
	if v, ok := cand.armScores[ArmSemantic]; ok {
		scores["semantic"] = v
	}
	if v, ok := cand.armScores[ArmKeyword]; ok {
		scores["keyword"] = v
	}
	// "reranker" is deliberately absent here: the caller fills it with the
	// reranker's own score, and only when the reranker actually scored this
	// candidate. Synthesising it from the best arm score would publish a
	// relevance number for a pipeline that has no reranker — and, worse, would
	// hand a rank-position reranker's pool a value it never produced, the very
	// misreading the rank-position rule exists to prevent (it must read None).
	return scores
}

// filterFloors drops results failing a post-ranking floor. The reranker and
// final floors act as per-result predicates; the retrieval floors constrain
// only their own arm, so they never act as an intersection here.
func filterFloors(candidates []*recallCandidate, minScores map[string]float64) []*recallCandidate {
	if len(minScores) == 0 {
		return candidates
	}
	out := make([]*recallCandidate, 0, len(candidates))
	for _, cand := range candidates {
		if matchesFloors(cand, minScores) {
			out = append(out, cand)
		}
	}
	return out
}

// retrievalArmStages are the min_scores keys that gate a retrieval arm rather
// than the result. A floor on one of these removes the fact from that arm; the
// fact survives if any other arm still surfaces it, matching the reference's
// per-arm gating (a fact disappears only when every arm excludes it).
var retrievalArmStages = map[string]bool{
	ArmSemantic: true,
	ArmKeyword:  true,
	ArmGraph:    true,
	ArmTemporal: true,
}

// matchesFloors reports whether a candidate survives the post-ranking floors.
// The reranker and final floors act as per-result predicates; a retrieval floor
// removes only its own arm, so a fact reached another way is kept.
func matchesFloors(cand *recallCandidate, minScores map[string]float64) bool {
	if len(minScores) == 0 {
		return true
	}
	if floor, ok := minScores["final"]; ok && cand.match.Final < floor {
		return false
	}
	if floor, ok := minScores["reranker"]; ok {
		if value, has := cand.match.Scores["reranker"]; has && value < floor {
			return false
		}
	}
	hasArmFloor := false
	for stage := range minScores {
		if retrievalArmStages[stage] {
			hasArmFloor = true
			break
		}
	}
	if !hasArmFloor {
		return true
	}
	// With an arm floor in play, the fact survives only if at least one arm
	// still surfaces it once the floored-out arms are dropped.
	for stage := range cand.armRanks {
		floor, floored := minScores[stage]
		if floored && retrievalArmStages[stage] {
			if score, ok := cand.armScores[stage]; ok && score < floor {
				continue
			}
		}
		return true
	}
	return false
}

// preferObservations drops raw facts superseded by a returned observation when
// the flag is set and both an observation type and a raw type are requested.
func preferObservations(candidates []*recallCandidate, q RecallQuery) []*recallCandidate {
	if !q.PreferObservations {
		return candidates
	}
	types := resolvedTypes(q.Types)
	if !containsString(types, string(TypeObservation)) {
		return candidates
	}
	superseded := map[string]bool{}
	for _, cand := range candidates {
		if cand.match.Type != string(TypeObservation) {
			continue
		}
		// An observation's source facts live in SourceMemoryIDs — the same ids
		// the response reports as source_fact_ids — so the dedicated field is
		// only a test seam; prefer the real one.
		ids := cand.match.ObservationSourceIDs
		if len(ids) == 0 {
			ids = cand.match.SourceMemoryIDs
		}
		for _, id := range ids {
			superseded[id] = true
		}
	}
	if len(superseded) == 0 {
		return candidates
	}
	out := make([]*recallCandidate, 0, len(candidates))
	for _, cand := range candidates {
		if cand.match.Type == string(TypeObservation) {
			out = append(out, cand)
			continue
		}
		if superseded[cand.match.ID] {
			continue
		}
		out = append(out, cand)
	}
	return out
}

// orderByFinal sorts candidates by descending final relevance, applying the
// post-ranking floors, then ties by descending reranker and finally by id.
func orderByFinal(candidates []*recallCandidate) []*recallCandidate {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].match.Final != candidates[j].match.Final {
			return candidates[i].match.Final > candidates[j].match.Final
		}
		return candidates[i].match.ID < candidates[j].match.ID
	})
	return candidates
}

// toMatch converts a fused candidate into a recall match for the envelope.
func (c *recallCandidate) toMatch() RecallMatch {
	m := c.match
	return m
}
