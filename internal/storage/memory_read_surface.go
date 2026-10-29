package storage

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"
)

// DocumentFactCounts returns the document's memory units grouped by fact type.
// Observations carry no document id of their own, so they are counted through
// the units the document owns: an observation belongs to the document when one
// of the document's units is among its sources.
func (s *MemoryStore) DocumentFactCounts(ctx context.Context, bankID, docID string) (map[string]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	counts := map[string]int{}
	documentUnits := map[string]bool{}
	for _, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == docID {
			counts[string(unit.FactType)]++
			documentUnits[unit.ID] = true
		}
	}
	observations := 0
	for _, unit := range s.units {
		if unit.BankID != bankID || unit.FactType != TypeObservation {
			continue
		}
		for _, source := range unit.SourceMemoryIDs {
			if documentUnits[source] {
				observations++
				break
			}
		}
	}
	counts[string(TypeObservation)] = observations
	return counts, nil
}

// EntityMentions returns the memory texts that mention an entity.
func (s *MemoryStore) EntityMentions(ctx context.Context, bankID, entityID string, limit int) ([]EntityMention, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	mentions := make([]EntityMention, 0)
	for unitID, entityIDs := range s.unitEntities {
		if !containsString(entityIDs, entityID) {
			continue
		}
		unit, ok := s.units[unitID]
		if !ok || unit.BankID != bankID {
			continue
		}
		mentions = append(mentions, EntityMention{Text: unit.Text, MentionedAt: unit.MentionedAt})
		if limit > 0 && len(mentions) >= limit {
			break
		}
	}
	return mentions, nil
}

// UnitEntities returns the canonical names of the entities one memory unit
// mentions, in the order the unit's postings were linked. The reference does not
// sort them: its UNION of direct and inherited rows has no ORDER BY
// (engine/memories/pg/graph.py:440,464-465 dedupes per unit while preserving
// arrival order), so neither does this one.
func (s *MemoryStore) UnitEntities(ctx context.Context, bankID, unitID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if unit, ok := s.units[unitID]; !ok || unit.BankID != bankID {
		return []string{}, nil
	}
	return s.entityNamesForUnitLocked(unitID), nil
}

// ListDocuments returns one page of a bank's documents plus the total.
func (s *MemoryStore) ListDocuments(ctx context.Context, query DocumentQuery) (DocumentPage, error) {
	if err := ctx.Err(); err != nil {
		return DocumentPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return DocumentPage{}, s.readErr
	}

	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAnyStrict
	}
	// The fields a `q` search matched on each document are kept beside the
	// match, because the page is taken after the whole bank has been scanned.
	searched := query.Query != ""
	matched := make([]Document, 0, len(s.documents))
	matches := make(map[string][]string, len(s.documents))
	for _, doc := range s.documents {
		if doc.BankID != query.BankID {
			continue
		}
		if !documentsTagFilter(doc.Tags, query.Tags, mode, query.TagGroups) {
			continue
		}
		if !documentWithinWindow(doc, query) {
			continue
		}
		if searched {
			fields := documentSearchMatches(doc, query.Query)
			if len(fields) == 0 {
				continue
			}
			matches[doc.ID] = fields
		}
		matched = append(matched, doc)
	}
	sortDocuments(matched, query)
	// A `q` search counts at most DocumentSearchCap matches and stops where the
	// count does, so no page serves a row the total disowns.
	total, estimate := len(matched), false
	if searched && total > DocumentSearchCap {
		total, estimate = DocumentSearchCap, true
	}
	start, end := documentsPageBounds(len(matched), query.Offset, query.Limit, searched)
	page := matched[start:end]
	// The list carries each document's current unit count, computed from the
	// units actually stored rather than from a stale counter, and its metadata
	// as the same derivation the detail read serves.
	for i := range page {
		page[i].MemoryUnitCount = s.countUnitsForDocumentLocked(page[i].BankID, page[i].ID)
		page[i].Metadata = DocumentMetadata(page[i].RetainParams)
		if searched {
			page[i].MatchedFields = matches[page[i].ID]
		}
	}
	return DocumentPage{
		Documents: page, Total: total, Limit: query.Limit, Offset: query.Offset, TotalIsEstimate: estimate,
	}, nil
}

// ListChunks returns one page of a bank's chunks plus the total.
func (s *MemoryStore) ListChunks(ctx context.Context, query ChunkQuery) (ChunkPage, error) {
	if err := ctx.Err(); err != nil {
		return ChunkPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return ChunkPage{}, s.readErr
	}
	matched := make([]Chunk, 0, len(s.chunks))
	for _, chunk := range s.chunks {
		if chunk.BankID != query.BankID {
			continue
		}
		if query.DocumentID != "" && chunk.DocumentID != query.DocumentID {
			continue
		}
		matched = append(matched, chunk)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].DocumentID != matched[j].DocumentID {
			return matched[i].DocumentID < matched[j].DocumentID
		}
		return matched[i].ChunkIndex < matched[j].ChunkIndex
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return ChunkPage{Chunks: matched[start:end], Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// GetChunk returns one stored chunk by identifier.
func (s *MemoryStore) GetChunk(ctx context.Context, bankID, chunkID string) (Chunk, error) {
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, chunk := range s.chunks {
		if chunk.BankID == bankID && chunk.ChunkID == chunkID {
			return chunk, nil
		}
	}
	return Chunk{}, ErrChunkNotFound
}

// FindChunk returns one stored chunk by identifier, wherever it is stored.
func (s *MemoryStore) FindChunk(ctx context.Context, chunkID string) (Chunk, error) {
	if err := ctx.Err(); err != nil {
		return Chunk{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, chunk := range s.chunks {
		if chunk.ChunkID == chunkID {
			return chunk, nil
		}
	}
	return Chunk{}, ErrChunkNotFound
}

// ListTags returns the bank's tags with their usage counts, most used first.
func (s *MemoryStore) ListTags(ctx context.Context, bankID string) ([]TagCount, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	counts := map[string]int{}
	for _, unit := range s.units {
		if unit.BankID != bankID {
			continue
		}
		for _, tag := range unit.Tags {
			counts[tag]++
		}
	}
	out := make([]TagCount, 0, len(counts))
	for tag, count := range counts {
		out = append(out, TagCount{Tag: tag, Count: count})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tag < out[j].Tag
	})
	return out, nil
}

// ListEntities returns one page of a bank's entities plus the total.
func (s *MemoryStore) ListEntities(ctx context.Context, query EntityQuery) (EntityPage, error) {
	if err := ctx.Err(); err != nil {
		return EntityPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return EntityPage{}, s.readErr
	}
	matched := make([]Entity, 0, len(s.entities))
	stats := s.entityStatsLocked(query)
	for _, entity := range s.entities {
		if entity.BankID != query.BankID {
			continue
		}
		if query.ID != "" && entity.ID != query.ID {
			continue
		}
		if stats != nil {
			stat, ok := stats[entity.ID]
			if !ok {
				continue
			}
			entity.MentionCount = stat.mentions
			entity.FirstSeen = stat.first
			entity.LastSeen = stat.last
		}
		matched = append(matched, entity)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].MentionCount != matched[j].MentionCount {
			return matched[i].MentionCount > matched[j].MentionCount
		}
		// last_seen DESC, with a missing stamp ordered first the way PostgreSQL
		// orders NULLs under a descending sort.
		left, right := matched[i].LastSeen, matched[j].LastSeen
		switch {
		case left == nil && right == nil:
		case left == nil:
			return true
		case right == nil:
			return false
		case !left.Equal(*right):
			return left.After(*right)
		}
		return matched[i].ID < matched[j].ID
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return EntityPage{Entities: matched[start:end], Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// entityStat is one entity's mention count and date span over a tag filter's
// matching memories.
type entityStat struct {
	mentions int
	first    *time.Time
	last     *time.Time
}

// entityStatsLocked returns each entity's mention count and date span over the
// units a query's tag predicate admits, or nil when no tag filter is active, so
// the caller keeps the stored columns.
func (s *MemoryStore) entityStatsLocked(query EntityQuery) map[string]*entityStat {
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAny
	}
	if !tagFilterActive(query.Tags, mode, query.TagGroups) {
		return nil
	}
	stats := map[string]*entityStat{}
	for _, unit := range s.units {
		if unit.BankID != query.BankID || !matchesTagFilter(unit.Tags, query.Tags, mode, query.TagGroups) {
			continue
		}
		seen := unitSeenAt(unit)
		for _, id := range s.mentionedEntityIDsLocked(unit, query.BankID) {
			stat, ok := stats[id]
			if !ok {
				stat = &entityStat{}
				stats[id] = stat
			}
			stat.mentions++
			if stat.first == nil || seen.Before(*stat.first) {
				stat.first = seen
			}
			if stat.last == nil || seen.After(*stat.last) {
				stat.last = seen
			}
		}
	}
	return stats
}

// unitSeenAt is the per-memory date retain stamps on an entity: the occurrence
// start, falling back to when it was mentioned, with the event date last (it is
// never absent).
func unitSeenAt(unit MemoryUnit) *time.Time {
	if unit.OccurredStart != nil {
		return unit.OccurredStart
	}
	if unit.MentionedAt != nil {
		return unit.MentionedAt
	}
	return unit.EventDate
}

// entityEventDate is the date an entity mention is stamped with, which the
// reference takes from the fact's own event time and falls back to the mention
// time: `fact.occurred_start if fact.occurred_start is not None else
// fact.mentioned_at` (engine/retain/entity_processing.py:107). It is narrower
// than unitSeenAt — `event_date` is never used — because a fact with neither an
// occurred start nor a mention time leaves the stamp to the write clock.
func entityEventDate(unit MemoryUnit) *time.Time {
	if unit.OccurredStart != nil {
		return unit.OccurredStart
	}
	return unit.MentionedAt
}

// UnitSeenAt reports when a memory unit was seen: its occurred start, else when
// it was mentioned, else its event date. It reports the zero time for a unit
// that carries no time at all, which a writer reads as "stamp the write time".
//
// It is the same time the entity graph's recency is read from, so a pair's
// last-co-occurrence and the unit that witnessed it agree.
func UnitSeenAt(unit MemoryUnit) time.Time {
	if stamp := unitSeenAt(unit); stamp != nil {
		return *stamp
	}
	return time.Time{}
}

// Graph returns the bank's memory graph for the query's filtered unit set. The
// filters select the units that become nodes and table rows before the limit is
// applied.
func (s *MemoryStore) Graph(ctx context.Context, query GraphQuery) (GraphData, error) {
	if err := ctx.Err(); err != nil {
		return GraphData{}, err
	}
	limit := query.Limit
	if limit < 0 {
		limit = 1000
	}
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAllStrict
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return GraphData{}, s.readErr
	}

	// Units are the graph's nodes, ordered newest first so a bounded read keeps
	// the most recent evidence rather than an arbitrary slice.
	units := make([]MemoryUnit, 0)
	for _, unit := range s.units {
		if unit.BankID != query.BankID {
			continue
		}
		if query.Type != "" && string(unit.FactType) != query.Type {
			continue
		}
		if query.Query != "" &&
			!strings.Contains(strings.ToLower(unit.Text), strings.ToLower(query.Query)) &&
			!strings.Contains(strings.ToLower(unit.Context), strings.ToLower(query.Query)) {
			continue
		}
		if query.DocumentID != "" && !s.unitMatchesSourceFieldLocked(unit, "document_id", query.DocumentID) {
			continue
		}
		if query.ChunkID != "" && !s.unitMatchesSourceFieldLocked(unit, "chunk_id", query.ChunkID) {
			continue
		}
		if !matchesTagFilter(unit.Tags, query.Tags, mode, query.TagGroups) {
			continue
		}
		units = append(units, unit)
	}
	sort.SliceStable(units, func(i, j int) bool {
		left, right := units[i].MentionedAt, units[j].MentionedAt
		switch {
		case left == nil && right == nil:
			// Fall through to the event-date tiebreak.
		case left == nil:
			return false // NULLS LAST under a descending sort.
		case right == nil:
			return true
		case !left.Equal(*right):
			return left.After(*right)
		}
		// event_date DESC; PostgreSQL orders NULLs first under a descending sort.
		leftDate, rightDate := units[i].EventDate, units[j].EventDate
		switch {
		case leftDate == nil && rightDate == nil:
			return false
		case leftDate == nil:
			return true
		case rightDate == nil:
			return false
		}
		if !leftDate.Equal(*rightDate) {
			return leftDate.After(*rightDate)
		}
		// The two leading keys tie, which PostgreSQL leaves unordered; break the
		// tie on the identifier so the node list — and the derived edge
		// direction that follows it — is the same on every render.
		return units[i].ID < units[j].ID
	})
	total := len(units)
	if len(units) > limit {
		units = units[:limit]
	}

	nodes := make([]GraphNodeData, 0, len(units))
	rows := make([]GraphTableRow, 0, len(units))
	for _, unit := range units {
		entities := s.entityNamesForUnitLocked(unit.ID)
		if len(entities) == 0 && unit.FactType == TypeObservation {
			entities = s.inheritedEntityNamesLocked(unit, query.BankID)
		}
		nodes = append(nodes, GraphNodeData{
			ID: unit.ID, Text: unit.Text, Context: unit.Context, FactType: string(unit.FactType),
			Entities: entities, DocumentID: unit.DocumentID, ChunkID: unit.ChunkID,
			Tags: unit.Tags, Mentioned: unit.MentionedAt, Date: unit.EventDate,
			CreatedAt: unit.CreatedAt, SourceMemoryIDs: unit.SourceMemoryIDs,
		})
		rows = append(rows, GraphTableRow{
			ID: unit.ID, Text: unit.Text, Context: unit.Context,
			OccurredStart: unit.OccurredStart, OccurredEnd: unit.OccurredEnd,
			MentionedAt: unit.MentionedAt, Date: unit.EventDate, CreatedAt: unit.CreatedAt,
			Entities:   strings.Join(entities, ", "),
			DocumentID: unit.DocumentID, ChunkID: unit.ChunkID, FactType: string(unit.FactType),
			Tags: unit.Tags, ProofCount: unit.ProofCount,
		})
	}

	// The stored links among the visible units and the source memories they
	// inherit from, strongest first and bounded, so the derived edges can be
	// built from one pass. An entity edge is derived, not stored.
	linkIDs := make(map[string]bool, len(units))
	for _, unit := range units {
		linkIDs[unit.ID] = true
		for _, source := range unit.SourceMemoryIDs {
			linkIDs[source] = true
		}
	}
	links := make([]GraphEdgeData, 0)
	for _, link := range s.links {
		if link.BankID != query.BankID || !linkIDs[link.FromUnitID] || !linkIDs[link.ToUnitID] {
			continue
		}
		links = append(links, GraphEdgeData{
			From: link.FromUnitID, To: link.ToUnitID, Type: string(link.LinkType), Weight: link.Weight,
		})
	}
	sort.SliceStable(links, func(i, j int) bool { return links[i].Weight > links[j].Weight })
	if len(links) > graphEdgeCap {
		links = links[:graphEdgeCap]
	}
	entityNames := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		if len(node.Entities) > 0 {
			entityNames[node.ID] = node.Entities
		}
	}
	edges := deriveGraphEdges(nodes, entityNames, links)
	return GraphData{Nodes: nodes, Edges: edges, TableRows: rows, Total: total, Limit: limit}, nil
}

// EntityGraph returns the bank's entity co-occurrence graph. Nodes are the
// endpoints of the returned edges; a tag filter recomputes mention counts and
// edges from the matching memories only.
func (s *MemoryStore) EntityGraph(ctx context.Context, query EntityGraphQuery) (EntityGraph, error) {
	if err := ctx.Err(); err != nil {
		return EntityGraph{}, err
	}
	limit := query.Limit
	if limit < 0 {
		limit = 1000
	}
	minCount := query.MinCount
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAny
	}
	bankID := query.BankID
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return EntityGraph{}, s.readErr
	}

	mentions := map[string]int{}
	weights := map[string]int{}
	seen := map[string]time.Time{}
	// The tag-filtered path recomputes everything from the matching memories; the
	// unfiltered path keeps the stored counters but still derives when each pair
	// was last seen together from the units that name it.
	filtered := tagFilterActive(query.Tags, mode, query.TagGroups)
	if filtered {
		for _, unit := range s.units {
			if unit.BankID != bankID || !matchesTagFilter(unit.Tags, query.Tags, mode, query.TagGroups) {
				continue
			}
			recordEntityCooccurrence(unit, s.mentionedEntityIDsLocked(unit, bankID), mentions, weights, seen)
		}
	} else {
		for _, entity := range s.entities {
			if entity.BankID == bankID {
				mentions[entity.ID] = entity.MentionCount
			}
		}
		for key, count := range s.cooccurrences {
			parts := strings.SplitN(key, "\x00", 3)
			if len(parts) != 3 || parts[0] != bankID {
				continue
			}
			weights[parts[1]+"\x00"+parts[2]] = count
		}
		for _, unit := range s.units {
			if unit.BankID != bankID {
				continue
			}
			recordEntityCooccurrence(unit, s.unitEntities[unit.ID], nil, nil, seen)
		}
	}

	type pair struct {
		from, to string
		weight   int
		seen     *time.Time
	}
	pairs := make([]pair, 0, len(weights))
	for key, count := range weights {
		if count < minCount {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		var lastSeen *time.Time
		if stamp, ok := seen[key]; ok {
			value := stamp
			lastSeen = &value
		}
		pairs = append(pairs, pair{from: parts[0], to: parts[1], weight: count, seen: lastSeen})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].weight != pairs[j].weight {
			return pairs[i].weight > pairs[j].weight
		}
		// last_cooccurred DESC, most recent first.
		left, right := pairs[i].seen, pairs[j].seen
		switch {
		case left == nil && right == nil:
			return false
		case left == nil:
			return false
		case right == nil:
			return true
		default:
			return left.After(*right)
		}
	})
	if len(pairs) > limit {
		pairs = pairs[:limit]
	}
	edges := make([]EntityGraphEdge, 0, len(pairs))
	for _, p := range pairs {
		edges = append(edges, EntityGraphEdge{
			ID: p.from + "->" + p.to, Source: p.from, Target: p.to, Weight: p.weight,
			LastCooccurred: p.seen,
		})
	}
	// Nodes are the endpoints of the returned edges, in edge-appearance order:
	// walking the edges strongest first, each contributes its two endpoints the
	// first time they are seen (the way the reference renders the graph,
	// engine/memories/pg/engine_curation.py:417-424). The postgres backend
	// builds them the same way.
	nodes := make([]EntityGraphNode, 0, 2*len(pairs))
	nodeSeen := map[string]bool{}
	for _, p := range pairs {
		for _, id := range []string{p.from, p.to} {
			if nodeSeen[id] {
				continue
			}
			nodeSeen[id] = true
			entity, ok := s.entities[id]
			if !ok {
				continue
			}
			nodes = append(nodes, EntityGraphNode{ID: id, Label: entity.CanonicalName, MentionCount: mentions[id]})
		}
	}
	return EntityGraph{Nodes: nodes, Edges: edges, TotalEntities: len(nodes),
		TotalEdges: len(edges), Limit: limit}, nil
}

// recordEntityCooccurrence folds one memory's entities into the graph's running
// counts, optionally accumulating how recently each pair was seen. A nil
// weights map means the caller keeps its stored counts and only wants the
// timestamps.
func recordEntityCooccurrence(unit MemoryUnit, entityIDs []string, mentions, weights map[string]int, seen map[string]time.Time) {
	stamp := unitSeenAt(unit)
	for _, id := range entityIDs {
		if mentions != nil {
			mentions[id]++
		}
	}
	for i := range entityIDs {
		for j := i + 1; j < len(entityIDs); j++ {
			a, b := entityIDs[i], entityIDs[j]
			if a == b {
				continue
			}
			if a > b {
				a, b = b, a
			}
			key := a + "\x00" + b
			if weights != nil {
				weights[key]++
			}
			if stamp != nil {
				if current, ok := seen[key]; !ok || stamp.After(current) {
					seen[key] = *stamp
				}
			}
		}
	}
}

// documentsTagFilter applies the reference's document tag predicate. Unlike the
// shared unit predicate, the plain any/all modes admit an untagged document:
// their clause is a disjunction over the row's own tags, not a requirement that
// the row carry any.
func documentsTagFilter(present, tags []string, mode string, groups []TagGroup) bool {
	if len(groups) > 0 {
		return matchTagGroups(present, groups)
	}
	switch mode {
	case TagsMatchAny, TagsMatchAll:
		if len(present) == 0 {
			return true
		}
	}
	if len(tags) == 0 && mode != TagsMatchExact {
		return true
	}
	return matchFlatTags(present, tags, mode)
}

// sortDocuments orders a document page the way the reference does: by the
// window axis descending when a window is asked for, and by last write first
// (updated_at, then created_at, then id) otherwise.
func sortDocuments(docs []Document, query DocumentQuery) {
	if axis, windowed := windowAxis(query); windowed {
		sort.SliceStable(docs, func(i, j int) bool {
			left, right := documentAxis(docs[i], axis), documentAxis(docs[j], axis)
			if !left.Equal(right) {
				return left.After(right)
			}
			return docs[i].ID < docs[j].ID
		})
		return
	}
	sort.SliceStable(docs, func(i, j int) bool {
		if !docs[i].UpdatedAt.Equal(docs[j].UpdatedAt) {
			return docs[i].UpdatedAt.After(docs[j].UpdatedAt)
		}
		if !docs[i].CreatedAt.Equal(docs[j].CreatedAt) {
			return docs[i].CreatedAt.After(docs[j].CreatedAt)
		}
		return docs[i].ID < docs[j].ID
	})
}

// unitMatchesSourceFieldLocked reports whether a unit is selected by a
// document_id/chunk_id filter: directly, or — the way the reference matches an
// observation — through one of the memories it was consolidated from.
func (s *MemoryStore) unitMatchesSourceFieldLocked(unit MemoryUnit, field, value string) bool {
	if field == "document_id" && unit.DocumentID == value {
		return true
	}
	if field == "chunk_id" && unit.ChunkID == value {
		return true
	}
	if unit.FactType != TypeObservation {
		return false
	}
	for _, sourceID := range unit.SourceMemoryIDs {
		source, ok := s.units[sourceID]
		if !ok {
			continue
		}
		if field == "document_id" && source.DocumentID == value {
			return true
		}
		if field == "chunk_id" && source.ChunkID == value {
			return true
		}
	}
	return false
}

// documentAxis reads the timestamp a windowed document query filters on.
func documentAxis(doc Document, axis string) time.Time {
	if axis == "created_at" {
		return doc.CreatedAt
	}
	return doc.UpdatedAt
}

// windowAxis reports the column a document query's window filters and orders
// by, and whether a window was requested at all.
func windowAxis(query DocumentQuery) (string, bool) {
	if query.TimeField == "" && query.TimeStart == nil && query.TimeEnd == nil {
		return "", false
	}
	if query.TimeField == "created_at" {
		return "created_at", true
	}
	return "updated_at", true
}

// documentWithinWindow reports whether a document's selected timestamp falls
// inside the query's optional bounds. A window orders and filters on one axis;
// rows with no value on that axis are excluded, and the window is half-open
// [start, end).
func documentWithinWindow(doc Document, query DocumentQuery) bool {
	axis, windowed := windowAxis(query)
	if !windowed {
		return true
	}
	instant := documentAxis(doc, axis)
	if query.TimeStart != nil && instant.Before(*query.TimeStart) {
		return false
	}
	if query.TimeEnd != nil && !instant.Before(*query.TimeEnd) {
		return false
	}
	return true
}

// countUnitsForDocumentLocked counts the units belonging to one document.
func (s *MemoryStore) countUnitsForDocumentLocked(bankID, documentID string) int {
	count := 0
	for _, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == documentID {
			count++
		}
	}
	return count
}

// UnitEntityNames returns the entities each unit mentions, keyed by unit id.
//
// An observation with no postings of its own inherits the entities of the
// memories it was consolidated from, which is the set the unit's own detail
// read reports.
func (s *MemoryStore) UnitEntityNames(ctx context.Context, bankID string, unitIDs []string) (map[string][]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	byUnit := make(map[string][]string, len(unitIDs))
	for _, unitID := range unitIDs {
		unit, ok := s.units[unitID]
		if !ok {
			unit, ok = s.invalidated[unitID]
		}
		if !ok || unit.BankID != bankID {
			continue
		}
		names := s.entityNamesForUnitLocked(unitID)
		if len(names) == 0 && unit.FactType == TypeObservation && len(unit.SourceMemoryIDs) > 0 {
			names = s.inheritedEntityNamesLocked(unit, bankID)
		}
		if len(names) > 0 {
			byUnit[unitID] = names
		}
	}
	return byUnit, nil
}

// inheritedEntityNamesLocked collects the entities of a unit's source memories,
// deduped, in the order the reference's UNION returns them, for an observation
// that has none of its own.
func (s *MemoryStore) inheritedEntityNamesLocked(unit MemoryUnit, bankID string) []string {
	inherited := make([]string, 0)
	for _, sourceID := range unit.SourceMemoryIDs {
		source, ok := s.units[sourceID]
		if !ok || source.BankID != bankID {
			continue
		}
		for _, name := range s.entityNamesForUnitLocked(sourceID) {
			if !slices.Contains(inherited, name) {
				inherited = append(inherited, name)
			}
		}
	}
	return inherited
}

// mentionedEntityIDsLocked returns the entity ids one unit contributes to a
// tag-filtered entity read: its own postings, or — the rule the reference's
// visible_entity_mentions_sql applies (engine/memories/pg/curation.py:539-583)
// — for an observation with no postings of its own, the postings of the
// memories it was consolidated from, deduplicated the way that UNION is.
// Without the inheritance an entity only observations mention would vanish
// under a filter.
func (s *MemoryStore) mentionedEntityIDsLocked(unit MemoryUnit, bankID string) []string {
	ids := s.unitEntities[unit.ID]
	if len(ids) > 0 || unit.FactType != TypeObservation {
		return ids
	}
	inherited := make([]string, 0)
	for _, sourceID := range unit.SourceMemoryIDs {
		source, ok := s.units[sourceID]
		if !ok || source.BankID != bankID {
			continue
		}
		for _, id := range s.unitEntities[sourceID] {
			if !slices.Contains(inherited, id) {
				inherited = append(inherited, id)
			}
		}
	}
	return inherited
}

// entityNamesForUnitLocked returns the names of the entities one unit mentions.
func (s *MemoryStore) entityNamesForUnitLocked(unitID string) []string {
	ids := s.unitEntities[unitID]
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if entity, ok := s.entities[id]; ok {
			out = append(out, entity.CanonicalName)
		}
	}
	return out
}

// unitEventDateOf narrows entityEventDate to the value form the entity writers
// take; a unit with no event time yields the zero time.
func unitEventDateOf(unit MemoryUnit) time.Time {
	if stamp := entityEventDate(unit); stamp != nil {
		return *stamp
	}
	return time.Time{}
}
