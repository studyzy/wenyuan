package storage

import (
	"context"
	"sort"
	"strings"
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
	ranked := scoreCandidates(candidates, arms, q)
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

// scoreCandidates fuses per-arm ranks and applies combined scoring. A boost
// configured for an arm promotes that arm's candidates before fusion and gives
// them a bounded post-rerank bump; unboosted arms keep the head of their own
// ranking.
func scoreCandidates(candidates map[string]*recallCandidate, arms []string, q RecallQuery) []*recallCandidate {
	list := make([]*recallCandidate, 0, len(candidates))
	for _, cand := range candidates {
		list = append(list, cand)
	}
	// Deterministic base order so equal scores resolve by id.
	sort.SliceStable(list, func(i, j int) bool { return list[i].match.ID < list[j].match.ID })

	for _, cand := range list {
		fused := 0.0
		for _, arm := range arms {
			if rank, ok := cand.armRanks[arm]; ok {
				fused += 1 / (rrfConstant + float64(rank))
			}
		}
		// Normalized reranker relevance: the best arm score for this
		// candidate, which is neutral when no arm surfaced a positive score.
		reranker := bestArmScore(cand)
		boost := combinedBoost(cand, q)
		final := fused * reranker * boost
		if reranker > 0 && boost == 0 {
			final = fused * reranker
		}
		cand.match.Final = final
		cand.match.ArmRanks = cloneIntMap(cand.armRanks)
		cand.match.Scores = candidateScores(cand)
	}
	return list
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

// combinedBoost multiplies bounded recency, temporal-proximity and
// evidence-count factors. Each factor is neutral (1) when its signal is absent.
func combinedBoost(cand *recallCandidate, q RecallQuery) float64 {
	if len(cand.armScores) == 0 {
		return 1
	}
	evidence := 1 + 0.05*float64(len(cand.armRanks)-1)
	if evidence < 1 {
		evidence = 1
	}
	proximity := 1.0
	if q.Temporal != nil && q.Temporal.Contains(cand.match.OccurredStart, cand.match.OccurredEnd) {
		proximity = 1.2
	}
	recency := 1.0
	if cand.match.MentionedAt != nil {
		recency = 1 + 0.1
	}
	boost := evidence * proximity * recency
	// Bounded post-rerank bump for a strategy-boosted arm.
	if level := boostLevelFor(cand, q.StrategyBoosts); level > 0 {
		boost *= 1 + 0.25*level
	}
	if boost == 0 {
		return 1
	}
	return boost
}

// boostLevelFor returns the numeric boost (1..3) for the strongest boosted arm
// a candidate was surfaced by, or 0 when none are boosted.
func boostLevelFor(cand *recallCandidate, boosts map[string]string) float64 {
	if len(boosts) == 0 {
		return 0
	}
	best := 0.0
	for arm := range cand.armRanks {
		switch strings.ToLower(strings.TrimSpace(boosts[arm])) {
		case "low":
			if best < 1 {
				best = 1
			}
		case "medium":
			if best < 2 {
				best = 2
			}
		case "high":
			if best < 3 {
				best = 3
			}
		}
	}
	return best
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
