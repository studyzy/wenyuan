package storage

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

// Recall runs the multi-arm retrieval and ranking pipeline for the in-process
// store. It returns a single ranked list whose order is descending by combined
// relevance, plus any requested side channels.
func (s *MemoryStore) Recall(ctx context.Context, q RecallQuery) (RecallResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return RecallResult{}, s.readErr
	}
	if _, ok := s.banks[q.BankID]; !ok {
		return RecallResult{}, ErrBankNotFound
	}
	types := resolvedTypes(q.Types)
	arms := activeArms(q)
	term := strings.TrimSpace(q.Query)
	// The budget level bounds the retrieval work, so it caps how many
	// candidates survive ranking unless the caller named an explicit limit.
	limit := q.Limit
	if limit <= 0 {
		limit = BudgetLimit(q.Budget)
	}

	// Per-arm best-first candidate lists.
	armHits := map[string][]recallHit{}
	for _, arm := range arms {
		armHits[arm] = s.runArm(arm, q, types, term)
	}

	// Merge candidates across arms, keeping the first rank observed per fact.
	candidates := map[string]*recallCandidate{}
	for _, arm := range arms {
		for rank, hit := range armHits[arm] {
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
			if _, seen := cand.armRanks[arm]; !seen {
				cand.armRanks[arm] = rank + 1
				cand.armScores[arm] = hit.score
			}
		}
	}

	// Fusion ordering (reciprocal rank fusion) then combined scoring.
	ranked := scoreCandidates(ctx, candidates, arms, q)
	ranked = orderByFinal(ranked)
	ranked = filterFloors(ranked, q.MinScores)
	ranked = preferObservations(ranked, q)
	ranked = capCandidates(ranked, limit)

	result := RecallResult{Matches: make([]RecallMatch, 0, len(ranked))}
	for _, cand := range ranked {
		result.Matches = append(result.Matches, cand.toMatch())
	}
	s.attachSideChannels(&result, q, types)
	result.Trace = buildRecallTrace(candidates, ranked)
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
// report it without re-deriving the counts from the ranked list.
func buildRecallTrace(candidates map[string]*recallCandidate, ranked []*recallCandidate) RecallTrace {
	// Every arm a candidate was surfaced by is counted, so the trace reports
	// which retrieval paths actually contributed rather than a fixed subset.
	stageCounts := map[string]int{}
	for _, cand := range ranked {
		for stage := range cand.armRanks {
			stageCounts[stage]++
		}
	}
	budget := len(candidates)
	return RecallTrace{
		Budget:          budget,
		BudgetUsed:      len(ranked),
		CandidatesFound: len(candidates),
		StageCounts:     stageCounts,
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
	// failure leaves every candidate on the best-per-arm fallback.
	reranked := rerankCandidates(ctx, list, q)

	for index, cand := range list {
		fused := 0.0
		for _, arm := range arms {
			if rank, ok := cand.armRanks[arm]; ok {
				fused += 1 / (rrfConstant + float64(rank))
			}
		}
		fused = stage1Boost(cand, fused, q.StrategyBoosts)
		// Relevance is the cross-encoder's score when one ran, normalized
		// against the batch so it lands on the same scale as the per-arm
		// fallback it replaces; otherwise the best arm score, which is neutral
		// when no arm surfaced a positive score.
		reranker := bestArmScore(cand)
		if reranked != nil {
			reranker = normalizeRerankScore(reranked[index], reranked)
		}
		boost := combinedBoost(cand, q)
		final := fused * reranker * boost
		if reranker > 0 && boost == 0 {
			final = fused * reranker
		}
		// The strategy boost is a post-rerank add, so it lands on the final
		// weight rather than multiplying into it.
		final += stage2Boost(cand, q.StrategyBoosts) * fused
		cand.match.Final = final
		cand.match.ArmRanks = cloneIntMap(cand.armRanks)
		cand.match.Scores = candidateScores(cand)
		if reranked != nil {
			cand.match.Scores["reranker"] = reranker
		}
	}
	return list
}

// rerankCandidates scores the fused pool through the cross-encoder boundary,
// returning one score per candidate in list order. It returns nil when no
// reranker is configured, the pool is empty, or the call failed — all of which
// leave the caller on its fallback.
func rerankCandidates(ctx context.Context, list []*recallCandidate, q RecallQuery) []float64 {
	if q.Rerank == nil || len(list) == 0 {
		return nil
	}
	if q.RerankDepth > 0 && len(list) > q.RerankDepth {
		// Only the depth the caller budgeted reaches the cross-encoder; the
		// rest keeps its fallback score, which is what a shallow reranker
		// means.
		list = list[:q.RerankDepth]
	}
	texts := make([]string, 0, len(list))
	for _, cand := range list {
		texts = append(texts, cand.match.Text)
	}
	scores, err := q.Rerank(ctx, q.Query, texts)
	if err != nil || len(scores) != len(texts) {
		return nil
	}
	// Pad the tail with the neutral zero so the slice indexes by candidate
	// position whether or not the pool was truncated to the rerank depth.
	if len(scores) < len(list) {
		scores = append(scores, make([]float64, len(list)-len(scores))...)
	}
	return scores
}

// normalizeRerankScore maps a reranked score onto [0, 1] using the batch's own
// range, so the value is comparable to the per-arm scores it replaces. A batch
// whose scores are all equal has no range to normalize against and yields the
// neutral 1, which leaves fusion ordering intact.
func normalizeRerankScore(score float64, scores []float64) float64 {
	if scores == nil {
		return 0
	}
	low, high := scores[0], scores[0]
	for _, value := range scores {
		if value < low {
			low = value
		}
		if value > high {
			high = value
		}
	}
	if high <= low {
		return 1
	}
	normalized := (score - low) / (high - low)
	// Keep the value strictly positive so the multiplier never zeroes a
	// candidate that fusion ranked, which would drop it from the result.
	return 0.01 + 0.99*normalized
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
	recencyAlpha      = 0.2
	temporalAlpha     = 0.2
	proofCountAlpha   = 0.1
	recencyHalflife   = 90.0
	recencyLinearDays = 365.0
)

// recencyDecayFunction selects the age-to-freshness curve. "linear" is the
// default; "exponential" halves the signal every recencyHalflife days; "none"
// disables the signal.
const recencyDecayFunction = "linear"

// recencyForMaps an age in days onto a freshness signal in [0, 1] whose neutral
// point is 0.5. A future-dated memory clamps to the maximum so it is never
// penalised for being ahead of the clock.
func recencySignal(daysAgo float64) float64 {
	switch recencyDecayFunction {
	case "none":
		return 0.5
	case "exponential":
		if daysAgo <= 0 {
			return 1
		}
		return math.Pow(0.5, daysAgo/recencyHalflife)
	default:
		if daysAgo <= 0 {
			return 1
		}
		return math.Max(0.1, math.Min(1, 1-daysAgo/recencyLinearDays))
	}
}

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
	// Recency is graded by the match's age rather than a flat present/absent
	// bump, so a fresh memory ranks above a stale one when relevance ties.
	recency := 0.5
	if instant := recallInstant(cand.match); instant != nil {
		anchor := time.Now().UTC()
		if q.QueryTimestamp != nil {
			anchor = *q.QueryTimestamp
		}
		recency = recencySignal(anchor.Sub(*instant).Hours() / 24)
	}
	// Temporal proximity is meaningful only for a query that named a window.
	temporal := 0.5
	if q.Temporal != nil && q.Temporal.Contains(cand.match.OccurredStart, cand.match.OccurredEnd) {
		temporal = 1
	}
	// Proof count measures evidence strength, which only an observation has.
	// The logarithmic curve keeps a large count from dominating: 1 proof is
	// neutral and the signal saturates well before the count is huge.
	proof := 0.5
	if count := len(cand.match.SourceFactIDs); count >= 1 {
		proof = math.Max(0, math.Min(1, 0.5+math.Log(float64(count))/10))
	}
	return confidenceBoost(recency, recencyAlpha) *
		confidenceBoost(temporal, temporalAlpha) *
		confidenceBoost(proof, proofCountAlpha)
}

// recallInstant returns the time a match should be aged from. It follows the
// same precedence the retrieval date uses, so a memory carrying only a mention
// time still gets a real recency signal rather than the neutral value.
func recallInstant(match RecallMatch) *time.Time {
	switch {
	case match.OccurredStart != nil:
		return match.OccurredStart
	case match.MentionedAt != nil:
		return match.MentionedAt
	default:
		return match.OccurredEnd
	}
}

// candidateScores builds the per-stage score object as a stage-keyed map. A
// stage that did not surface the candidate reports no value.
func candidateScores(cand *recallCandidate) map[string]float64 {
	scores := map[string]float64{}
	if v, ok := cand.armScores[ArmSemantic]; ok {
		scores["semantic"] = v
	}
	if v, ok := cand.armScores[ArmKeyword]; ok {
		scores["keyword"] = v
	}
	if best := bestArmScore(cand); best > 0 {
		scores["reranker"] = best
	}
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

// matchesFloors reports whether a candidate clears every post-ranking floor.
// A stage the candidate carries no value for is not floored.
func matchesFloors(cand *recallCandidate, minScores map[string]float64) bool {
	if len(minScores) == 0 {
		return true
	}
	for stage, floor := range minScores {
		switch stage {
		case "final":
			if cand.match.Final < floor {
				return false
			}
		case "reranker":
			if v, ok := cand.match.Scores["reranker"]; ok && v < floor {
				return false
			}
		default:
			// semantic and keyword floors constrain only their own arm; a
			// result reached another way is not excluded.
			if cand.armRanks[stage] > 0 {
				if score, ok := cand.armScores[stage]; ok && score < floor {
					return false
				}
			}
		}
	}
	return true
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
		for _, id := range cand.match.ObservationSourceIDs {
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
