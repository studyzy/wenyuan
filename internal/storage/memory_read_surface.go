package storage

import (
	"context"
	"sort"
	"strings"
)

// DocumentFactCounts returns the document's memory units grouped by fact type.
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
	for _, unit := range s.units {
		if unit.BankID == bankID && unit.DocumentID == docID {
			counts[string(unit.FactType)]++
		}
	}
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
// mentions, in name order.
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
	matched := make([]Document, 0, len(s.documents))
	for _, doc := range s.documents {
		if doc.BankID != query.BankID {
			continue
		}
		if len(query.Tags) > 0 && !matchFlatTags(doc.Tags, query.Tags, mode) {
			continue
		}
		// A compound filter — the caller's resolved read scope — must also
		// admit the document, so a scoped reader sees only its own documents.
		if len(query.TagGroups) > 0 && !matchesTagFilter(doc.Tags, query.Tags, mode, query.TagGroups) {
			continue
		}
		if !documentWithinWindow(doc, query) {
			continue
		}
		if term := strings.ToLower(strings.TrimSpace(query.Query)); term != "" &&
			!strings.Contains(strings.ToLower(doc.ID), term) {
			continue
		}
		matched = append(matched, doc)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	page := matched[start:end]
	// The list carries each document's current unit count, computed from the
	// units actually stored rather than from a stale counter.
	for i := range page {
		page[i].MemoryUnitCount = s.countUnitsForDocumentLocked(page[i].BankID, page[i].ID)
	}
	return DocumentPage{Documents: page, Total: total, Limit: query.Limit, Offset: query.Offset}, nil
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
	counts := s.entityMentionCountsLocked(query)
	for _, entity := range s.entities {
		if entity.BankID != query.BankID {
			continue
		}
		if query.ID != "" && entity.ID != query.ID {
			continue
		}
		if counts != nil {
			count, ok := counts[entity.ID]
			if !ok {
				continue
			}
			entity.MentionCount = count
		}
		matched = append(matched, entity)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].MentionCount != matched[j].MentionCount {
			return matched[i].MentionCount > matched[j].MentionCount
		}
		return matched[i].ID < matched[j].ID
	})
	total := len(matched)
	start, end := pageBounds(total, query.Offset, query.Limit)
	return EntityPage{Entities: matched[start:end], Total: total, Limit: query.Limit, Offset: query.Offset}, nil
}

// entityMentionCountsLocked returns per-entity mention counts over the units a
// query's tag predicate admits, or nil when no tag filter is active, so the
// caller keeps the stored counts.
func (s *MemoryStore) entityMentionCountsLocked(query EntityQuery) map[string]int {
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAny
	}
	if !tagFilterActive(query.Tags, mode, query.TagGroups) {
		return nil
	}
	counts := map[string]int{}
	for _, unit := range s.units {
		if unit.BankID != query.BankID || !matchesTagFilter(unit.Tags, query.Tags, mode, query.TagGroups) {
			continue
		}
		for _, id := range s.unitEntities[unit.ID] {
			counts[id]++
		}
	}
	return counts
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
		if term := strings.ToLower(strings.TrimSpace(query.Query)); term != "" &&
			!strings.Contains(strings.ToLower(unit.Text), term) &&
			!strings.Contains(strings.ToLower(unit.Context), term) {
			continue
		}
		if query.DocumentID != "" && unit.DocumentID != query.DocumentID {
			continue
		}
		if query.ChunkID != "" && unit.ChunkID != query.ChunkID {
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
			return units[i].ID < units[j].ID
		case left == nil:
			return false
		case right == nil:
			return true
		default:
			return left.After(*right)
		}
	})
	total := len(units)
	if len(units) > limit {
		units = units[:limit]
	}

	kept := map[string]bool{}
	nodes := make([]GraphNodeData, 0, len(units))
	rows := make([]GraphTableRow, 0, len(units))
	for _, unit := range units {
		kept[unit.ID] = true
		entities := s.entityNamesForUnitLocked(unit.ID)
		nodes = append(nodes, GraphNodeData{
			ID: unit.ID, Text: unit.Text, Context: unit.Context, FactType: string(unit.FactType),
			Entities: entities, DocumentID: unit.DocumentID, ChunkID: unit.ChunkID,
			Tags: unit.Tags, Mentioned: unit.MentionedAt, Date: unit.EventDate,
		})
		rows = append(rows, GraphTableRow{
			ID: unit.ID, Text: unit.Text, Context: unit.Context,
			OccurredStart: unit.OccurredStart, OccurredEnd: unit.OccurredEnd,
			MentionedAt: unit.MentionedAt, Date: unit.EventDate,
			Entities:   strings.Join(entities, ","),
			DocumentID: unit.DocumentID, ChunkID: unit.ChunkID, FactType: string(unit.FactType),
			Tags: unit.Tags, ProofCount: 1,
		})
	}

	// An edge is kept only when both of its endpoints are in the bounded node
	// set, so the client never receives a dangling reference.
	edges := make([]GraphEdgeData, 0)
	for _, link := range s.links {
		if link.BankID != query.BankID || !kept[link.FromUnitID] || !kept[link.ToUnitID] {
			continue
		}
		edges = append(edges, GraphEdgeData{
			ID: link.FromUnitID + "->" + link.ToUnitID, From: link.FromUnitID, To: link.ToUnitID, Type: string(link.LinkType),
		})
	}
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
	if minCount < 1 {
		minCount = 1
	}
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
	if tagFilterActive(query.Tags, mode, query.TagGroups) {
		for _, unit := range s.units {
			if unit.BankID != bankID || !matchesTagFilter(unit.Tags, query.Tags, mode, query.TagGroups) {
				continue
			}
			ids := s.unitEntities[unit.ID]
			for _, id := range ids {
				mentions[id]++
			}
			for i := range ids {
				for j := i + 1; j < len(ids); j++ {
					a, b := ids[i], ids[j]
					if a == b {
						continue
					}
					if a > b {
						a, b = b, a
					}
					weights[a+"\x00"+b]++
				}
			}
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
	}

	type pair struct {
		from, to string
		weight   int
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
		pairs = append(pairs, pair{from: parts[0], to: parts[1], weight: count})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].weight != pairs[j].weight {
			return pairs[i].weight > pairs[j].weight
		}
		if pairs[i].from != pairs[j].from {
			return pairs[i].from < pairs[j].from
		}
		return pairs[i].to < pairs[j].to
	})
	if len(pairs) > limit {
		pairs = pairs[:limit]
	}
	seen := map[string]bool{}
	edges := make([]EntityGraphEdge, 0, len(pairs))
	for _, p := range pairs {
		seen[p.from] = true
		seen[p.to] = true
		edges = append(edges, EntityGraphEdge{ID: p.from + "->" + p.to, Source: p.from, Target: p.to, Weight: p.weight})
	}
	nodes := make([]EntityGraphNode, 0, len(seen))
	for id := range seen {
		entity, ok := s.entities[id]
		if !ok {
			continue
		}
		nodes = append(nodes, EntityGraphNode{ID: id, Label: entity.CanonicalName, MentionCount: mentions[id]})
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].MentionCount != nodes[j].MentionCount {
			return nodes[i].MentionCount > nodes[j].MentionCount
		}
		return nodes[i].ID < nodes[j].ID
	})
	return EntityGraph{Nodes: nodes, Edges: edges, TotalEntities: len(nodes),
		TotalEdges: len(edges), Limit: limit}, nil
}

// documentWithinWindow reports whether a document's selected timestamp falls
// inside the query's optional bounds. An absent bound is unbounded on that
// side.
func documentWithinWindow(doc Document, query DocumentQuery) bool {
	instant := doc.CreatedAt
	if query.TimeField == "updated_at" {
		instant = doc.UpdatedAt
	}
	if query.TimeStart != nil && instant.Before(*query.TimeStart) {
		return false
	}
	if query.TimeEnd != nil && instant.After(*query.TimeEnd) {
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
	sort.Strings(out)
	return out
}
