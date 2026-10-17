package storage

// graphEdgeCap bounds the stored links one graph render reads, matching the
// reference's own cap on the edge count it will build.
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

	// Entity edges: pair visible units that share an entity, each unit linked to
	// a bounded window of the entity's unit list so a hot entity does not produce
	// a quadratic edge set. First-appearance order keeps the output stable.
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

	// Semantic edges: observations sharing a source memory, in source-memory
	// first-appearance order.
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

	all := make([]GraphEdgeData, 0, len(direct)+len(copied)+len(inferred))
	all = append(all, direct...)
	all = append(all, copied...)
	all = append(all, inferred...)
	out := make([]GraphEdgeData, 0, len(all))
	seenEdge := map[string]bool{}
	for _, edge := range all {
		key := edge.From + "\x00" + edge.To + "\x00" + edge.Type + "\x00" + edge.Entity
		if seenEdge[key] {
			continue
		}
		seenEdge[key] = true
		out = append(out, edge)
	}
	return out
}
