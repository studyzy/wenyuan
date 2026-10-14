package storage

import (
	"context"
	"sort"
	"strings"
)

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

	matched := make([]Document, 0, len(s.documents))
	for _, doc := range s.documents {
		if doc.BankID != query.BankID {
			continue
		}
		if !documentHasEveryTag(doc, query.Tags) {
			continue
		}
		if term := strings.ToLower(strings.TrimSpace(query.Query)); term != "" &&
			!strings.Contains(strings.ToLower(doc.OriginalText), term) {
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
	for _, doc := range s.documents {
		if doc.BankID != bankID {
			continue
		}
		for _, tag := range doc.Tags {
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
	for _, entity := range s.entities {
		if entity.BankID != query.BankID {
			continue
		}
		if query.Kind != "" && entity.EntityKind != query.Kind {
			continue
		}
		if term := strings.ToLower(strings.TrimSpace(query.Query)); term != "" &&
			!strings.Contains(strings.ToLower(entity.CanonicalName), term) {
			continue
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

// Graph returns the bank's memory graph.
func (s *MemoryStore) Graph(ctx context.Context, bankID string, limit int) (GraphData, error) {
	if err := ctx.Err(); err != nil {
		return GraphData{}, err
	}
	if limit <= 0 {
		limit = 100
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
		if unit.BankID == bankID {
			units = append(units, unit)
		}
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
			ID: unit.ID, Text: unit.Text, FactType: string(unit.FactType),
			Entities: entities, DocumentID: unit.DocumentID, ChunkID: unit.ChunkID,
			Tags: unit.Tags, Mentioned: unit.MentionedAt,
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
		if link.BankID != bankID || !kept[link.FromUnitID] || !kept[link.ToUnitID] {
			continue
		}
		edges = append(edges, GraphEdgeData{
			ID: link.FromUnitID + "->" + link.ToUnitID, From: link.FromUnitID, To: link.ToUnitID, Type: string(link.LinkType),
		})
	}
	return GraphData{Nodes: nodes, Edges: edges, TableRows: rows, Total: total, Limit: limit}, nil
}

// EntityGraph returns the bank's entity graph.
func (s *MemoryStore) EntityGraph(ctx context.Context, bankID string, limit int) (EntityGraph, error) {
	if err := ctx.Err(); err != nil {
		return EntityGraph{}, err
	}
	if limit <= 0 {
		limit = 200
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return EntityGraph{}, s.readErr
	}

	entities := make([]Entity, 0)
	for _, entity := range s.entities {
		if entity.BankID == bankID {
			entities = append(entities, entity)
		}
	}
	sort.SliceStable(entities, func(i, j int) bool {
		if entities[i].MentionCount != entities[j].MentionCount {
			return entities[i].MentionCount > entities[j].MentionCount
		}
		return entities[i].ID < entities[j].ID
	})
	totalEntities := len(entities)
	if len(entities) > limit {
		entities = entities[:limit]
	}
	kept := map[string]bool{}
	nodes := make([]EntityGraphNode, 0, len(entities))
	for _, entity := range entities {
		kept[entity.ID] = true
		nodes = append(nodes, EntityGraphNode{ID: entity.ID, Label: entity.CanonicalName})
	}

	edges := make([]EntityGraphEdge, 0)
	for key, count := range s.cooccurrences {
		parts := strings.SplitN(key, "\x00", 3)
		if len(parts) != 3 || parts[0] != bankID {
			continue
		}
		if !kept[parts[1]] || !kept[parts[2]] {
			continue
		}
		edges = append(edges, EntityGraphEdge{
			ID: parts[1] + "->" + parts[2], Source: parts[1], Target: parts[2], Weight: count,
		})
	}
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return EntityGraph{Nodes: nodes, Edges: edges, TotalEntities: totalEntities,
		TotalEdges: len(edges), Limit: limit}, nil
}

// documentHasEveryTag reports whether a document carries all the listed tags.
// An empty list matches everything.
func documentHasEveryTag(doc Document, tags []string) bool {
	if len(tags) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, tag := range doc.Tags {
		have[tag] = true
	}
	for _, tag := range tags {
		if !have[tag] {
			return false
		}
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
