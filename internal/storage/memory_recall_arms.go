package storage

import (
	"math"
	"sort"
)

func (s *MemoryStore) runArm(arm string, q RecallQuery, types []string, term string) []recallHit {
	if arm == ArmKeyword && !hasWordChar(term) {
		return nil
	}
	// The graph arm does not score memories by their own text: it reaches the
	// ones connected to what the lexical arms already matched. It therefore
	// runs from those seeds rather than from the bank's rows.
	if arm == ArmGraph {
		hits := s.runGraphArm(q, types, term)
		sortHits(hits)
		return hits
	}
	hits := make([]recallHit, 0)
	for _, m := range s.memories[q.BankID] {
		if !containsString(types, m.Type) {
			continue
		}
		if !s.tagScopeAllows(q, m) {
			continue
		}
		score, ok := armScore(arm, m, term)
		if !ok {
			continue
		}
		hits = append(hits, recallHit{match: m, score: score})
	}
	if arm == ArmTemporal {
		hits = s.spreadTemporal(hits, q.BankID)
	}
	sortHits(hits)
	return hits
}

// sortHits orders arm results best-first, breaking ties on the identifier so
// the ranking is stable across runs.
func sortHits(hits []recallHit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return hits[i].match.ID < hits[j].match.ID
		}
		return hits[i].score > hits[j].score
	})
}

// Graph-arm bounds. Spreading walks outward from the lexical seeds, so it is
// bounded in both depth and breadth: an unbounded walk from a well-connected
// seed would drag in most of the bank.
const (
	graphHopDecay     = 0.6
	graphNodeBudget   = 64
	graphIterationCap = 2
)

// runGraphArm reaches the memories connected to the memories the lexical arms
// already matched, scoring each by the strength of the path that reached it.
//
// A result surfaced here is neither semantically nor lexically similar to the
// query — that is the point, since a fact linked to a relevant one is often
// relevant itself without sharing its wording.
func (s *MemoryStore) runGraphArm(q RecallQuery, types []string, term string) []recallHit {
	if term == "" || len(s.links) == 0 {
		return nil
	}
	seeds := s.graphSeeds(q, types, term)
	if len(seeds) == 0 {
		return nil
	}
	reached := s.spreadFromSeeds(seeds, q.BankID)
	seedsSeen := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		seedsSeen[seed.match.ID] = true
	}
	hits := make([]recallHit, 0, len(reached))
	for _, m := range s.memories[q.BankID] {
		// A seed is already surfaced by its own arm, so it is not re-emitted
		// here: doing so would double-count it in the fusion.
		if seedsSeen[m.ID] || !containsString(types, m.Type) {
			continue
		}
		if score, ok := reached[m.ID]; ok {
			hits = append(hits, recallHit{match: m, score: score})
		}
	}
	return hits
}

// graphSeeds collects the memories the lexical arms match, which are where the
// graph walk starts.
func (s *MemoryStore) graphSeeds(q RecallQuery, types []string, term string) []recallHit {
	seeds := make([]recallHit, 0)
	for _, m := range s.memories[q.BankID] {
		if !containsString(types, m.Type) || !s.tagScopeAllows(q, m) {
			continue
		}
		if score, ok := armScore(ArmSemantic, m, term); ok && score > 0 {
			seeds = append(seeds, recallHit{match: m, score: score})
		}
	}
	sortHits(seeds)
	return seeds
}

// spreadFromSeeds walks the link graph outward from the seed scores, keeping the
// strongest path to each memory reached.
func (s *MemoryStore) spreadFromSeeds(seeds []recallHit, bankID string) map[string]float64 {
	best := make(map[string]float64, len(seeds))
	for _, seed := range seeds {
		best[seed.match.ID] = seed.score
	}
	outgoing := s.outgoingLinks(bankID)
	frontier := make([]string, 0, len(best))
	for id := range best {
		frontier = append(frontier, id)
	}
	sort.Strings(frontier)

	visited := 0
	for iteration := 0; iteration < graphIterationCap && len(frontier) > 0; iteration++ {
		next := make([]string, 0)
		for _, id := range frontier {
			if visited >= graphNodeBudget {
				break
			}
			visited++
			for _, link := range outgoing[id] {
				propagated := best[id] * graphHopDecay * relationStrength(link.LinkType)
				if propagated <= 0 {
					continue
				}
				if existing, ok := best[link.ToUnitID]; ok && existing >= propagated {
					continue
				}
				best[link.ToUnitID] = propagated
				next = append(next, link.ToUnitID)
			}
		}
		sort.Strings(next)
		frontier = next
	}
	return best
}

// outgoingLinks indexes the bank's links by source unit.
func (s *MemoryStore) outgoingLinks(bankID string) map[string][]MemoryLink {
	out := make(map[string][]MemoryLink)
	for _, link := range s.links {
		if link.BankID != bankID {
			continue
		}
		out[link.FromUnitID] = append(out[link.FromUnitID], link)
	}
	return out
}

// Temporal-spreading bounds for the temporal arm.
const (
	temporalHopDecay     = 0.5
	temporalNodeBudget   = 64
	temporalIterationCap = 3
)

// spreadTemporal propagates scores outward from the temporal arm's seeds
// through temporal and causal links, applying a per-hop decay and a relation
// strength multiplier so a causal edge propagates further than co-occurrence in
// time. Spreading is bounded by a node budget and an iteration cap, and keeps
// the strongest path per target so a target's score does not depend on link
// order.
func (s *MemoryStore) spreadTemporal(seeds []recallHit, bankID string) []recallHit {
	if len(seeds) == 0 || len(s.links) == 0 {
		return seeds
	}
	best := map[string]float64{}
	for _, hit := range seeds {
		best[hit.match.ID] = hit.score
	}
	// Index outgoing links by source for the bank.
	out := map[string][]MemoryLink{}
	for _, link := range s.links {
		if link.BankID != bankID {
			continue
		}
		out[link.FromUnitID] = append(out[link.FromUnitID], link)
	}
	frontier := make([]string, 0, len(best))
	for id := range best {
		frontier = append(frontier, id)
	}
	sort.Strings(frontier)
	visited := 0
	for iteration := 0; iteration < temporalIterationCap && len(frontier) > 0; iteration++ {
		next := []string{}
		for _, id := range frontier {
			if visited >= temporalNodeBudget {
				break
			}
			visited++
			for _, link := range out[id] {
				propagated := best[id] * temporalHopDecay * relationStrength(link.LinkType)
				if propagated <= 0 {
					continue
				}
				if existing, ok := best[link.ToUnitID]; ok && existing >= propagated {
					continue
				}
				best[link.ToUnitID] = propagated
				next = append(next, link.ToUnitID)
			}
		}
		sort.Strings(next)
		frontier = next
	}
	// Emit every target reached by spreading that is a stored memory.
	hits := make([]recallHit, 0, len(best))
	for _, m := range s.memories[bankID] {
		if score, ok := best[m.ID]; ok {
			hits = append(hits, recallHit{match: m, score: score})
		}
	}
	return hits
}

// relationStrength returns the multiplier for a link type; causal edges
// propagate further than temporal or semantic ones.
func relationStrength(linkType MemoryLinkType) float64 {
	switch linkType {
	case LinkCauses, LinkCausedBy:
		return 1.0
	case LinkEnables:
		return 0.8
	case LinkPrevents:
		return 0.7
	case LinkTemporal:
		return 0.5
	default:
		return 0.3
	}
}

// armScore scores one memory for one arm. The boolean result reports whether
// the arm surfaced the memory at all. The lexical arms require query-word
// relevance; the temporal and entity arms surface time-anchored and
// entity-bearing memories so a query with no searchable words still recalls
// through those arms (beh-0018) while a query with no signal at all returns an
// empty list (beh-0002).
func armScore(arm string, m RecallMatch, term string) (float64, bool) {
	switch arm {
	case ArmSemantic:
		if term == "" || sharedWordCount(m.Text, term) == 0 {
			return 0, false
		}
		return semanticScore(m, term), true
	case ArmKeyword:
		if term == "" {
			return 0, false
		}
		score := keywordScore(m, term)
		return score, score > 0
	case ArmTemporal:
		score := temporalScore(m)
		return score, score > 0
	case ArmEntity:
		if len(m.Entities) == 0 {
			return 0, false
		}
		return math.Max(entityScore(m, term), 0.01), true
	default:
		return 0, false
	}
}

// sharedWordCount counts the distinct query words appearing in a text.
func sharedWordCount(text, term string) int {
	q := wordSet(term)
	t := wordSet(text)
	n := 0
	for w := range q {
		if t[w] {
			n++
		}
	}
	return n
}

// semanticScore is a deterministic stand-in for vector similarity: shared word
// overlap between query and text, bounded to [0,1].
func semanticScore(m RecallMatch, term string) float64 {
	if term == "" {
		return 0
	}
	q := wordSet(term)
	t := wordSet(m.Text)
	if len(q) == 0 || len(t) == 0 {
		return 0
	}
	shared := 0
	for w := range q {
		if t[w] {
			shared++
		}
	}
	return float64(shared) / float64(len(q))
}

// keywordScore is a deterministic stand-in for lexical relevance: 1 when the
// query term occurs as a substring, decaying with position.
func keywordScore(m RecallMatch, term string) float64 {
	if term == "" {
		return 0
	}
	idx := indexFold(m.Text, term)
	if idx < 0 {
		return 0
	}
	return 1 / float64(1+idx)
}

// temporalScore ranks a memory by recency of its mention timestamp.
func temporalScore(m RecallMatch) float64 {
	if m.MentionedAt == nil {
		return 0
	}
	// More recent mentions score higher; a fixed reference date keeps the
	// ranking deterministic without wall-clock dependence.
	ref := m.MentionedAt.UTC().Unix()
	return 1 / float64(1+abs64(ref%86400))
}

// entityScore rates a memory by how many of the query words name an entity it
// carries.
func entityScore(m RecallMatch, term string) float64 {
	if term == "" || len(m.Entities) == 0 {
		return 0
	}
	q := wordSet(term)
	score := 0.0
	for _, e := range m.Entities {
		for w := range wordSet(e) {
			if q[w] {
				score += 1
				break
			}
		}
	}
	if score == 0 {
		return 0
	}
	return score / float64(len(m.Entities))
}
