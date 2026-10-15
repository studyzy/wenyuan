package storage

import "strings"

// FusionInterleave is the fusion mode that round-robins the retrieval arms
// instead of summing their reciprocal ranks.
const FusionInterleave = "interleave"

// prepareArm applies an arm's own score floor and then its per-source cap.
//
// The floor is applied *here*, before fusion, rather than only after it: a hit
// that fails its arm's floor must not enter the pool at all, or it would consume
// a share of the candidate budget and displace a result that clears every floor.
// The post-fusion check still runs, because a candidate reached through another
// arm is not excluded by this arm's floor.
//
// The caller is responsible for having sorted the hits; the cap only slices.
func prepareArm(q RecallQuery, arm string, hits []recallHit) []recallHit {
	if floor, ok := q.MinScores[arm]; ok && floor > 0 {
		filtered := make([]recallHit, 0, len(hits))
		for _, hit := range hits {
			if hit.score >= floor {
				filtered = append(filtered, hit)
			}
		}
		hits = filtered
	}
	return capArm(q.PerSourceCap, hits)
}

// capArm truncates one arm's best-first hits to the per-source cap, so one
// over-expanding backend cannot crowd the others out of the merged pool before
// the pool is trimmed to the candidate budget.
func capArm(perSourceCap int, hits []recallHit) []recallHit {
	if perSourceCap <= 0 || len(hits) <= perSourceCap {
		return hits
	}
	return hits[:perSourceCap]
}

// interleaveFusion orders candidates by round-robin across arms: every arm's
// first hit, then every arm's second, and so on, de-duplicating as it goes.
//
// The arm order is the priority order, so the strongest arm's best hit is
// always first. It is the alternative to reciprocal rank fusion for dedup-style
// recall: RRF scores by the sum of reciprocal ranks, so a candidate that is
// first in one arm but absent from the others is averaged down — which is
// exactly how a near-identical existing observation, sharing neither a graph
// link nor much vocabulary, gets buried below the budget cutoff.
//
// The order is reproduced from the recorded per-arm ranks, so it needs no
// second copy of the arm lists.
func interleaveFusion(candidates []*recallCandidate, arms []string) []*recallCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	// Bucket by arm and rank: byArm[arm][rank] is the candidate at that rank.
	byArm := map[string]map[int]*recallCandidate{}
	maxRank := 0
	for _, cand := range candidates {
		for _, arm := range arms {
			rank, ok := cand.armRanks[arm]
			if !ok {
				continue
			}
			if byArm[arm] == nil {
				byArm[arm] = map[int]*recallCandidate{}
			}
			// A candidate can hold only one rank per arm, so a collision means
			// the map was built wrong; keep the better (lower) rank.
			if existing, taken := byArm[arm][rank]; taken && existing.match.ID < cand.match.ID {
				continue
			}
			byArm[arm][rank] = cand
			if rank > maxRank {
				maxRank = rank
			}
		}
	}
	ordered := make([]*recallCandidate, 0, len(candidates))
	seen := map[string]bool{}
	for rank := 1; rank <= maxRank; rank++ {
		for _, arm := range arms {
			cand, ok := byArm[arm][rank]
			if !ok || seen[cand.match.ID] {
				continue
			}
			seen[cand.match.ID] = true
			ordered = append(ordered, cand)
		}
	}
	// Any candidate without a recorded rank in an active arm is still a
	// candidate; it goes last rather than being dropped from the results.
	for _, cand := range candidates {
		if !seen[cand.match.ID] {
			seen[cand.match.ID] = true
			ordered = append(ordered, cand)
		}
	}
	return ordered
}

// usesInterleave reports whether the query asked for interleave fusion.
func usesInterleave(q RecallQuery) bool {
	return strings.EqualFold(strings.TrimSpace(q.Fusion), FusionInterleave)
}
