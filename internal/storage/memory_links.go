package storage

import "sort"

// maxTemporalLinksPerUnit mirrors the reference's MAX_TEMPORAL_LINKS_PER_UNIT:
// the outgoing temporal edges one unit may hold before graph maintenance stops
// probing for more neighbours.
const maxTemporalLinksPerUnit = 20

// maxSemanticLinksPerUnit is the reference's MAX_SEMANTIC_LINKS_PER_UNIT: the
// per-unit ceiling on semantic neighbours it keeps, which is both the ANN
// probe's top_k at retain time and the budget the relink pass tops up to
// (`memories/pg/graph.py:61`).
const maxSemanticLinksPerUnit = 50

// temporalLinkWindowHours is the window graph maintenance re-derives temporal
// edges within, mirroring the 24h window retain enforces.
const temporalLinkWindowHours = 24

// temporalEligibleFactType reports whether a unit's kind takes part in temporal
// linking. Only dated evidence — an experience or a world fact — is linked by
// time; an observation never is.
func temporalEligibleFactType(t MemoryUnitType) bool {
	return t == TypeWorld || t == TypeExperience
}

// temporalLinkWeight grades a temporal edge by the hours between two units'
// dates, clamped at the floor the reference uses: max(0.3, 1 - h/24).
func temporalLinkWeight(hours float64) float64 {
	if hours < 0 {
		hours = -hours
	}
	weight := 1 - hours/float64(temporalLinkWindowHours)
	if weight < 0.3 {
		return 0.3
	}
	return weight
}

// hasDirectedLink reports whether a directed edge of one type joins two units.
func hasDirectedLink(links []MemoryLink, from, to string, kind MemoryLinkType) bool {
	for _, link := range links {
		if link.FromUnitID == from && link.ToUnitID == to && link.LinkType == kind {
			return true
		}
	}
	return false
}

// countOutgoingLinks counts the edges one unit sends of one type.
func countOutgoingLinks(links []MemoryLink, unitID string, kind MemoryLinkType) int {
	count := 0
	for _, link := range links {
		if link.FromUnitID == unitID && link.LinkType == kind {
			count++
		}
	}
	return count
}

// causalLinkTypes lists the causal link types, canonical form first and then
// the legacy names, matching the reference's CAUSAL_LINK_TYPES. An edit
// preserves exactly these edges: they are retain-time extraction output that
// nothing recreates, unlike the temporal and semantic edges graph maintenance
// re-derives.
var causalLinkTypes = []MemoryLinkType{
	LinkCausedBy, LinkCauses, LinkEnables, LinkPrevents,
}

// IsCausalLinkType reports whether a link type is causal, and so one an edit
// must keep.
func IsCausalLinkType(t MemoryLinkType) bool {
	for _, causal := range causalLinkTypes {
		if t == causal {
			return true
		}
	}
	return false
}

// causalLinkTypeNames returns the causal link types as strings, in the stable
// order causalLinkTypes declares them, for a SQL parameter.
func causalLinkTypeNames() []string {
	names := make([]string, len(causalLinkTypes))
	for i, causal := range causalLinkTypes {
		names[i] = string(causal)
	}
	return names
}

// dropNonCausalLinksFor removes every link incident to one unit except the
// causal edges. It is the in-memory backend's half of the reference's
// apply_edit link sweep, which drops the DERIVED edges — graph maintenance
// rebuilds the temporal and semantic ones that still hold — while keeping the
// causal edges, which no path recreates.
func dropNonCausalLinksFor(links []MemoryLink, unitID string) []MemoryLink {
	kept := links[:0]
	for _, link := range links {
		if link.FromUnitID != unitID && link.ToUnitID != unitID {
			kept = append(kept, link)
			continue
		}
		if IsCausalLinkType(link.LinkType) {
			kept = append(kept, link)
		}
	}
	return kept
}

// semanticLinksFrom returns the semantic links one unit should send to the
// others whose embedding is within the similarity floor, capped at
// semanticLinkFanOut per unit. It is the single-unit form a relink needs: a
// relink re-derives the edges of the unit it was queued for, not every pair in
// the candidate window.
func semanticLinksFrom(bankID string, unit MemoryUnit, others []MemoryUnit, minSimilarity float64) []MemoryLink {
	if len(unit.Embedding) == 0 {
		return nil
	}
	type scored struct {
		id         string
		similarity float64
	}
	neighbours := make([]scored, 0)
	for j := range others {
		if others[j].ID == unit.ID || len(others[j].Embedding) == 0 {
			continue
		}
		similarity := cosineBetween(unit.Embedding, others[j].Embedding)
		if similarity >= minSimilarity {
			neighbours = append(neighbours, scored{id: others[j].ID, similarity: similarity})
		}
	}
	sort.SliceStable(neighbours, func(a, b int) bool {
		if neighbours[a].similarity != neighbours[b].similarity {
			return neighbours[a].similarity > neighbours[b].similarity
		}
		return neighbours[a].id < neighbours[b].id
	})
	if len(neighbours) > maxSemanticLinksPerUnit {
		neighbours = neighbours[:maxSemanticLinksPerUnit]
	}
	// A unit is its own nearest neighbour, so the reference records a self-link
	// of weight 1 for every unit it probes; the statistics and the graph both
	// count it, so leaving it out would undercount both.
	links := make([]MemoryLink, 0, len(neighbours)+1)
	links = append(links, MemoryLink{
		BankID:     bankID,
		FromUnitID: unit.ID,
		ToUnitID:   unit.ID,
		LinkType:   LinkSemantic,
		Weight:     1,
	})
	for _, neighbour := range neighbours {
		links = append(links, MemoryLink{
			BankID:     bankID,
			FromUnitID: unit.ID,
			ToUnitID:   neighbour.id,
			LinkType:   LinkSemantic,
			Weight:     neighbour.similarity,
		})
	}
	return links
}

// appendMissingLinks appends the additions the store does not already hold, so
// a relink that runs twice — or over an edge that survived — never duplicates
// one. The stored graph is scanned by (from, to, type), which is what the
// reference's unique index on memory_links enforces.
func appendMissingLinks(links, additions []MemoryLink) []MemoryLink {
	if len(additions) == 0 {
		return links
	}
	existing := make(map[linkKey]bool, len(links))
	for _, link := range links {
		existing[linkKey{from: link.FromUnitID, to: link.ToUnitID, kind: link.LinkType}] = true
	}
	for _, link := range additions {
		key := linkKey{from: link.FromUnitID, to: link.ToUnitID, kind: link.LinkType}
		if existing[key] {
			continue
		}
		existing[key] = true
		links = append(links, link)
	}
	return links
}

// linkKey identifies an edge the way the store's unique index does.
type linkKey struct {
	from string
	to   string
	kind MemoryLinkType
}

// keysOf returns a map's keys as a fresh slice, for a caller that needs the set
// as an argument list.
func keysOf[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
