package storage

import (
	"sort"
)

// graphEdgeCap bounds one graph render's edge budget, matching the reference's
// GRAPH_MAX_EDGES (engine/memories/base.py:807).
const graphEdgeCap = 10000

// maxGraphNeighborsPerUnit bounds how many of an entity's other units one unit
// is linked to, matching the per-unit cap the historical writer used.
const maxGraphNeighborsPerUnit = 10

// deriveGraphEdges assembles the memory graph's edges from the links stored
// between units and the read-time links the reference derives.
//
// The stored links cover both endpoints in the visible set or among the source
// memories the visible observations were built from. On top of them the
// reference derives three more kinds:
//
//   - copied links: an observation inherits its source memories' links, so the
//     graph shows what an observation is about without duplicating the rows;
//   - entity edges: any two visible units sharing an entity, each unit linked to
//     up to maxGraphNeighborsPerUnit of its successors per entity;
//   - semantic edges: two observations that share at least one source memory.
//
// units are the visible nodes (with their source memory ids and entity names);
// links are the stored links among the visible units or their sources.
func deriveGraphEdges(units []GraphNodeData, entities map[string][]string, links []GraphEdgeData) []GraphEdgeData {
	unitSet := make(map[string]bool, len(units))
	sourceToObservations := map[string][]string{}
	for _, unit := range units {
		unitSet[unit.ID] = true
		for _, source := range unit.SourceMemoryIDs {
			sourceToObservations[source] = append(sourceToObservations[source], unit.ID)
		}
	}

	copied := make([]GraphEdgeData, 0)
	for _, link := range links {
		fromObservations := sourceToObservations[link.From]
		toObservations := sourceToObservations[link.To]
		if len(fromObservations) > 0 {
			for _, observation := range fromObservations {
				if !unitSet[link.To] && len(toObservations) == 0 {
					continue
				}
				target := link.To
				if len(toObservations) > 0 && !unitSet[link.To] {
					target = toObservations[0]
				}
				if unitSet[target] && observation != target {
					copied = append(copied, GraphEdgeData{
						From: observation, To: target, Type: link.Type, Weight: link.Weight, Entity: link.Entity,
					})
				}
			}
		}
		if len(toObservations) > 0 && unitSet[link.From] {
			for _, observation := range toObservations {
				if link.From != observation {
					copied = append(copied, GraphEdgeData{
						From: link.From, To: observation, Type: link.Type, Weight: link.Weight, Entity: link.Entity,
					})
				}
			}
		}
	}

	direct := make([]GraphEdgeData, 0, len(links))
	for _, link := range links {
		if unitSet[link.From] && unitSet[link.To] {
			direct = append(direct, link)
		}
	}

	seenInferred := map[string]bool{}
	inferred := make([]GraphEdgeData, 0)
	addInferred := func(from, to, linkType, entity string) {
		low, high := from, to
		if low > high {
			low, high = high, low
		}
		key := low + "\x00" + high + "\x00" + linkType + "\x00" + entity
		if seenInferred[key] {
			return
		}
		seenInferred[key] = true
		inferred = append(inferred, GraphEdgeData{From: from, To: to, Type: linkType, Weight: 1, Entity: entity})
	}

	// Semantic edges come first: the reference chains semantic_links() ahead of
	// entity_links() (engine/memory_engine.py:2691-2694), so observations
	// sharing a source memory, in source-memory first-appearance order, are the
	// derived edges a budget keeps.
	observationsBySource := map[string][]string{}
	var semanticOrder []string
	for _, unit := range units {
		if unit.FactType != string(TypeObservation) {
			continue
		}
		for _, source := range unit.SourceMemoryIDs {
			if _, seen := observationsBySource[source]; !seen {
				semanticOrder = append(semanticOrder, source)
			}
			observationsBySource[source] = append(observationsBySource[source], unit.ID)
		}
	}
	for _, source := range semanticOrder {
		ids := observationsBySource[source]
		for i := range ids {
			for j := i + 1; j < len(ids); j++ {
				addInferred(ids[i], ids[j], "semantic", "")
			}
		}
	}

	// Entity edges follow: pair visible units that share an entity, each unit
	// linked to a bounded window of the entity's unit list so a hot entity does
	// not produce a quadratic edge set. The groups are walked ascending by unit
	// count, ties in first-appearance order (engine/memory_engine.py:2679 sorts
	// the same way: rarest entity first, since a name in every memory links
	// everything to everything and says nothing).
	entityUnits := map[string][]string{}
	var entityOrder []string
	for _, unit := range units {
		for _, name := range entities[unit.ID] {
			if _, seen := entityUnits[name]; !seen {
				entityOrder = append(entityOrder, name)
			}
			entityUnits[name] = append(entityUnits[name], unit.ID)
		}
	}
	sort.SliceStable(entityOrder, func(i, j int) bool {
		return len(entityUnits[entityOrder[i]]) < len(entityUnits[entityOrder[j]])
	})
	for _, name := range entityOrder {
		ids := entityUnits[name]
		for i := range ids {
			limit := i + 1 + maxGraphNeighborsPerUnit
			if limit > len(ids) {
				limit = len(ids)
			}
			for j := i + 1; j < limit; j++ {
				addInferred(ids[i], ids[j], "entity", name)
			}
		}
	}

	// The reference assembles the edges in two buckets, each with its own
	// GRAPH_MAX_EDGES budget and one duplicate set shared across both
	// (engine/memory_engine.py:2704-2737): the stored links — direct rows
	// first, then the copies observations inherit — and then the derived edges.
	// A bucket stops adding at its budget, so the same edges survive truncation
	// on both sides and a well-linked bank keeps room for derived edges.
	edges := make([]GraphEdgeData, 0, len(links)+len(inferred))
	seenEdge := map[string]bool{}
	addBucket := func(rows []GraphEdgeData) {
		added := 0
		for _, row := range rows {
			if added >= graphEdgeCap {
				return
			}
			key := row.From + "\x00" + row.To + "\x00" + row.Type + "\x00" + row.Entity
			if seenEdge[key] {
				continue
			}
			seenEdge[key] = true
			added++
			edges = append(edges, row)
		}
	}
	stored := make([]GraphEdgeData, 0, len(direct)+len(copied))
	stored = append(stored, direct...)
	stored = append(stored, copied...)
	addBucket(stored)
	addBucket(inferred)
	return edges
}
