package storage

import (
	"math"
	"sort"
	"strings"
	"time"
)

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

// The temporal arm is an independent retrieval arm: when a request names a
// window it builds its own similarity-ranked pool, spreads entry points across
// the window, and walks the link graph outward. The limits below bound that
// work the same way on both backends.
const (
	temporalPoolSize        = 60
	temporalEntryPoints     = 10
	temporalCoverageBuckets = 8
	temporalMaxHops         = 5
	temporalSpreadDecay     = 0.7
	temporalContinueFloor   = 0.2
	temporalPerSourceLimit  = 10
	temporalBatchSize       = 20
	temporalMinLinkWeight   = 0.1
)

// temporalRow is one similarity-ranked row in the temporal arm's ANN pool.
type temporalRow struct {
	match      RecallMatch
	similarity float64
}

// temporalLink is one hop the temporal arm may follow. target carries the
// linked unit's row so the walk can emit it as a result.
type temporalLink struct {
	from   string
	weight float64
	kind   MemoryLinkType
	target temporalRow
}

// temporalBoost is the relation-strength multiplier a link carries when a
// temporal score propagates across it. Causal edges say more about temporal
// succession than mere co-occurrence in time.
func temporalBoost(kind MemoryLinkType) float64 {
	switch kind {
	case LinkCauses, LinkCausedBy:
		return 2.0
	case LinkEnables, LinkPrevents:
		return 1.5
	default:
		return 1.0
	}
}

// temporalLinkKind reports whether a link participates in temporal spreading.
func temporalLinkKind(kind MemoryLinkType) bool {
	switch kind {
	case LinkTemporal, LinkCauses, LinkCausedBy, LinkEnables, LinkPrevents:
		return true
	default:
		return false
	}
}

// temporalCoalesceDate is the unit's effective time for coverage bucketing.
func temporalCoalesceDate(m RecallMatch) *time.Time {
	if d := finiteUTC(m.OccurredStart); d != nil {
		return d
	}
	if d := finiteUTC(m.MentionedAt); d != nil {
		return d
	}
	return finiteUTC(m.OccurredEnd)
}

// temporalProximityValue grades a unit's date against the window midpoint:
// the midpoint scores 1, either edge 0, and a unit with no date is neutral at
// noDate. A zero-length window reads every dated unit as on the midpoint.
func temporalProximityValue(start, end time.Time, m RecallMatch, noDate float64) float64 {
	best := unitTemporalPoint(m.OccurredStart, m.OccurredEnd, m.MentionedAt)
	if best == nil {
		return noDate
	}
	span := end.Sub(start)
	if span <= 0 {
		return 1
	}
	mid := start.Add(span / 2)
	days := math.Abs(best.Sub(mid).Hours() / 24)
	halfDays := span.Hours() / 24 / 2
	return 1 - math.Min(days/halfDays, 1)
}

// selectTemporalCoverage picks limit entry points from a similarity-ranked
// pool, spread across nBuckets equal time slices of the window. Each tier
// takes the tier-th best item from every populated bucket, strongest first,
// so every slice is represented before any contributes a second item. Dates
// that all land in one bucket collapse to plain similarity order.
func selectTemporalCoverage(pool []temporalRow, start, end time.Time, limit, nBuckets int) []temporalRow {
	if len(pool) <= limit {
		return pool
	}
	ranked := append([]temporalRow(nil), pool...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].similarity != ranked[j].similarity {
			return ranked[i].similarity > ranked[j].similarity
		}
		return ranked[i].match.ID < ranked[j].match.ID
	})
	span := end.Sub(start).Seconds()
	buckets := map[int][]temporalRow{}
	order := make([]int, 0, nBuckets)
	for _, row := range ranked {
		b := 0
		if d := temporalCoalesceDate(row.match); d != nil && span > 0 {
			frac := d.Sub(start).Seconds() / span
			b = int(frac * float64(nBuckets))
			if b < 0 {
				b = 0
			}
			if b > nBuckets-1 {
				b = nBuckets - 1
			}
		}
		if _, seen := buckets[b]; !seen {
			order = append(order, b)
		}
		buckets[b] = append(buckets[b], row)
	}
	selected := make([]temporalRow, 0, limit)
	for tier := 0; len(selected) < limit; tier++ {
		tierRows := make([]temporalRow, 0, len(order))
		for _, b := range order {
			if len(buckets[b]) > tier {
				tierRows = append(tierRows, buckets[b][tier])
			}
		}
		if len(tierRows) == 0 {
			break
		}
		sort.SliceStable(tierRows, func(i, j int) bool {
			if tierRows[i].similarity != tierRows[j].similarity {
				return tierRows[i].similarity > tierRows[j].similarity
			}
			return tierRows[i].match.ID < tierRows[j].match.ID
		})
		for _, row := range tierRows {
			if len(selected) >= limit {
				break
			}
			selected = append(selected, row)
		}
	}
	return selected
}

// temporalArmHit builds one temporal arm result carrying its propagated score
// and window proximity. The arm's own score is the ANN similarity, which is
// what the arm's floor gates on.
func temporalArmHit(row temporalRow, temporalScore, proximity float64) recallHit {
	return recallHit{
		match:             row.match,
		score:             row.similarity,
		temporalScore:     &temporalScore,
		temporalProximity: &proximity,
	}
}

// temporalNeighbors resolves the eligible links leaving a batch of source ids.
// A backend may resolve them from one preloaded set; the spread applies the
// per-source cap itself so both backends agree on the bound.
type temporalNeighbors func(fromIDs []string) []temporalLink

// spreadTemporalArm walks one fact type's temporal entry points outward along
// links. A hop's propagated score is the parent's temporal score scaled by the
// stored weight, the relation boost and one hop of decay; a target continues
// the walk only when its combined temporal score clears the floor. Results are
// emitted in discovery order.
func spreadTemporalArm(entries []temporalRow, start, end time.Time, budget int, factType string, neighbors temporalNeighbors) []recallHit {
	results := make([]recallHit, 0, len(entries))
	visited := make(map[string]bool, len(entries))
	nodeTemporal := make(map[string]float64, len(entries))
	frontier := make([]string, 0, len(entries))
	for _, ep := range entries {
		id := ep.match.ID
		visited[id] = true
		proximity := temporalProximityValue(start, end, ep.match, 0.5)
		results = append(results, temporalArmHit(ep, proximity, proximity))
		nodeTemporal[id] = 1.0
		frontier = append(frontier, id)
	}
	remaining := budget - len(entries)
	propagated := func(l temporalLink) float64 {
		parent, ok := nodeTemporal[l.from]
		if !ok {
			parent = 0.5
		}
		return parent * l.weight * temporalBoost(l.kind) * temporalSpreadDecay
	}
	for hop := 0; hop < temporalMaxHops && len(frontier) > 0 && remaining > 0; hop++ {
		batch := frontier
		if len(batch) > temporalBatchSize {
			batch = batch[:temporalBatchSize]
		}
		frontier = frontier[len(batch):]
		links := neighbors(batch)
		// Keep only the strongest perSourceLimit links from each source.
		perSource := map[string][]temporalLink{}
		for _, l := range links {
			if !temporalLinkKind(l.kind) || l.weight < temporalMinLinkWeight {
				continue
			}
			if l.target.match.Type != factType {
				continue
			}
			perSource[l.from] = append(perSource[l.from], l)
		}
		capped := make([]temporalLink, 0, len(links))
		for _, group := range perSource {
			sort.SliceStable(group, func(i, j int) bool {
				if group[i].weight != group[j].weight {
					return group[i].weight > group[j].weight
				}
				return group[i].target.match.ID < group[j].target.match.ID
			})
			if len(group) > temporalPerSourceLimit {
				group = group[:temporalPerSourceLimit]
			}
			capped = append(capped, group...)
		}
		// Several eligible links can reach one target; keep the strongest path.
		best := map[string]temporalLink{}
		for _, l := range capped {
			id := l.target.match.ID
			if visited[id] {
				continue
			}
			if current, taken := best[id]; !taken || propagated(l) > propagated(current) {
				best[id] = l
			}
		}
		type scored struct {
			id    string
			link  temporalLink
			score float64
		}
		ranked := make([]scored, 0, len(best))
		for id, l := range best {
			ranked = append(ranked, scored{id: id, link: l, score: propagated(l)})
		}
		sort.SliceStable(ranked, func(i, j int) bool {
			if ranked[i].score != ranked[j].score {
				return ranked[i].score > ranked[j].score
			}
			return ranked[i].id < ranked[j].id
		})
		for _, item := range ranked {
			visited[item.id] = true
			remaining--
			proximity := temporalProximityValue(start, end, item.link.target.match, 0.3)
			combined := math.Max(proximity, item.score)
			results = append(results, temporalArmHit(item.link.target, combined, proximity))
			if remaining > 0 && combined > temporalContinueFloor {
				nodeTemporal[item.id] = combined
				frontier = append(frontier, item.id)
			}
			if remaining <= 0 {
				break
			}
		}
	}
	return results
}

// capTemporalPool keeps the strongest poolSize rows per fact type from a
// similarity-descending pool.
func capTemporalPool(rows []temporalRow, poolSize int) []temporalRow {
	counts := map[string]int{}
	out := make([]temporalRow, 0, len(rows))
	for _, row := range rows {
		if counts[row.match.Type] >= poolSize {
			continue
		}
		counts[row.match.Type]++
		out = append(out, row)
	}
	return out
}
