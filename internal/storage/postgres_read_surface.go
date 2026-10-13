package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// clampLimit applies the shared page bound, so a caller cannot ask for an
// unbounded read.
func clampLimit(limit, fallback int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > 500 {
		return 500
	}
	return limit
}

// ListDocuments returns one page of a bank's documents plus the total.
func (s *PostgresStore) ListDocuments(ctx context.Context, query DocumentQuery) (DocumentPage, error) {
	limit := clampLimit(query.Limit, 50)
	filter := `bank_id = $1
	           AND ($2 = '' OR original_text ILIKE '%' || $2 || '%')
	           AND ($3::text[] IS NULL OR tags @> $3)`
	var total int
	countQuery := `SELECT COUNT(*) FROM ` + quoteIdent(s.schema) + `.documents WHERE ` + filter
	tags := nullableTagArray(query.Tags)
	if err := s.db.QueryRowContext(ctx, countQuery, query.BankID, strings.TrimSpace(query.Query), tags).Scan(&total); err != nil {
		return DocumentPage{}, err
	}
	listQuery := `SELECT id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''),
	                     COALESCE(metadata, '{}'::jsonb), COALESCE(retain_params, '{}'::jsonb),
	                     COALESCE(tags, '{}'), created_at, created_at,
	                     (SELECT COUNT(*) FROM ` + quoteIdent(s.schema) + `.memory_units u
	                      WHERE u.bank_id = d.bank_id AND u.document_id = d.id)
	              FROM ` + quoteIdent(s.schema) + `.documents d
	              WHERE ` + filter + `
	              ORDER BY created_at DESC, id ASC LIMIT $4 OFFSET $5`
	rows, err := s.db.QueryContext(ctx, listQuery, query.BankID, strings.TrimSpace(query.Query), tags, limit, query.Offset)
	if err != nil {
		return DocumentPage{}, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Document, 0)
	for rows.Next() {
		doc, err := scanDocumentRow(rows)
		if err != nil {
			return DocumentPage{}, err
		}
		out = append(out, doc)
	}
	return DocumentPage{Documents: out, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

// ListChunks returns one page of a bank's chunks plus the total.
func (s *PostgresStore) ListChunks(ctx context.Context, query ChunkQuery) (ChunkPage, error) {
	limit := clampLimit(query.Limit, 100)
	filter := `bank_id = $1 AND ($2 = '' OR document_id = $2)`
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+quoteIdent(s.schema)+`.chunks WHERE `+filter,
		query.BankID, query.DocumentID).Scan(&total); err != nil {
		return ChunkPage{}, err
	}
	listQuery := `SELECT id, bank_id, COALESCE(document_id,''), COALESCE(index,0), COALESCE(text,''),
	                     COALESCE(content_hash,''), created_at
	              FROM ` + quoteIdent(s.schema) + `.chunks
	              WHERE ` + filter + `
	              ORDER BY document_id ASC, index ASC LIMIT $3 OFFSET $4`
	rows, err := s.db.QueryContext(ctx, listQuery, query.BankID, query.DocumentID, limit, query.Offset)
	if err != nil {
		return ChunkPage{}, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Chunk, 0)
	for rows.Next() {
		chunk, err := scanChunkRow(rows)
		if err != nil {
			return ChunkPage{}, err
		}
		out = append(out, chunk)
	}
	return ChunkPage{Chunks: out, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

// GetChunk returns one stored chunk by identifier.
func (s *PostgresStore) GetChunk(ctx context.Context, bankID, chunkID string) (Chunk, error) {
	query := `SELECT id, bank_id, COALESCE(document_id,''), COALESCE(index,0), COALESCE(text,''),
	                 COALESCE(content_hash,''), created_at
	          FROM ` + quoteIdent(s.schema) + `.chunks WHERE bank_id = $1 AND id = $2`
	chunk, err := scanChunkRow(s.db.QueryRowContext(ctx, query, bankID, chunkID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Chunk{}, ErrChunkNotFound
		}
		return Chunk{}, err
	}
	return chunk, nil
}

// ListTags returns the bank's tags with their usage counts, most used first.
func (s *PostgresStore) ListTags(ctx context.Context, bankID string) ([]TagCount, error) {
	query := `SELECT tag, COUNT(*) FROM (
	              SELECT unnest(COALESCE(tags, '{}')) AS tag
	              FROM ` + quoteIdent(s.schema) + `.documents WHERE bank_id = $1
	          ) AS expanded
	          GROUP BY tag ORDER BY COUNT(*) DESC, tag ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]TagCount, 0)
	for rows.Next() {
		var entry TagCount
		if err := rows.Scan(&entry.Tag, &entry.Count); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// ListEntities returns one page of a bank's entities plus the total.
func (s *PostgresStore) ListEntities(ctx context.Context, query EntityQuery) (EntityPage, error) {
	limit := clampLimit(query.Limit, 50)
	filter := `bank_id = $1 AND ($2 = '' OR kind = $2)
	           AND ($3 = '' OR name ILIKE '%' || $3 || '%')`
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+quoteIdent(s.schema)+`.entities WHERE `+filter,
		query.BankID, string(query.Kind), strings.TrimSpace(query.Query)).Scan(&total); err != nil {
		return EntityPage{}, err
	}
	listQuery := `SELECT id, bank_id, COALESCE(name,''), COALESCE(kind,''),
	                     '{}'::jsonb, first_seen, last_seen, COALESCE(mention_count,0)
	              FROM ` + quoteIdent(s.schema) + `.entities
	              WHERE ` + filter + `
	              ORDER BY mention_count DESC, id ASC LIMIT $4 OFFSET $5`
	rows, err := s.db.QueryContext(ctx, listQuery, query.BankID, string(query.Kind),
		strings.TrimSpace(query.Query), limit, query.Offset)
	if err != nil {
		return EntityPage{}, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Entity, 0)
	for rows.Next() {
		entity, err := scanEntityRow(rows)
		if err != nil {
			return EntityPage{}, err
		}
		out = append(out, entity)
	}
	return EntityPage{Entities: out, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

// Graph returns the bank's memory graph.
func (s *PostgresStore) Graph(ctx context.Context, bankID string, limit int) (GraphData, error) {
	limit = clampLimit(limit, 100)
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+quoteIdent(s.schema)+`.memory_units WHERE bank_id = $1`,
		bankID).Scan(&total); err != nil {
		return GraphData{}, err
	}
	unitQuery := `SELECT id, COALESCE(text,''), COALESCE(type,''), COALESCE(context,''),
	                     occurred_start, occurred_end, mentioned_at, event_date,
	                     COALESCE(document_id,''), COALESCE(chunk_id,''), COALESCE(tags, '{}')
	              FROM ` + quoteIdent(s.schema) + `.memory_units
	              WHERE bank_id = $1
	              ORDER BY mentioned_at DESC NULLS LAST, id ASC LIMIT $2`
	rows, err := s.db.QueryContext(ctx, unitQuery, bankID, limit)
	if err != nil {
		return GraphData{}, err
	}
	defer func() { _ = rows.Close() }()

	nodes := make([]GraphNodeData, 0)
	tableRows := make([]GraphTableRow, 0)
	kept := map[string]bool{}
	for rows.Next() {
		var row GraphTableRow
		var occurredStart, occurredEnd, mentionedAt, eventDate sql.NullTime
		var tagsRaw []byte
		if err := rows.Scan(&row.ID, &row.Text, &row.FactType, &row.Context,
			&occurredStart, &occurredEnd, &mentionedAt, &eventDate,
			&row.DocumentID, &row.ChunkID, &tagsRaw); err != nil {
			return GraphData{}, err
		}
		row.OccurredStart = nullTimePtr(occurredStart)
		row.OccurredEnd = nullTimePtr(occurredEnd)
		row.MentionedAt = nullTimePtr(mentionedAt)
		row.Date = nullTimePtr(eventDate)
		row.Tags = decodePostgresTextArray(tagsRaw)
		row.ProofCount = 1
		kept[row.ID] = true
		tableRows = append(tableRows, row)
		nodes = append(nodes, GraphNodeData{
			ID: row.ID, Text: row.Text, FactType: row.FactType,
			DocumentID: row.DocumentID, ChunkID: row.ChunkID, Tags: row.Tags,
			Mentioned: row.MentionedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return GraphData{}, err
	}

	// An edge is kept only when both endpoints are inside the bounded node set.
	edgeQuery := `SELECT id, bank_id, COALESCE(from_id,''), COALESCE(to_id,''), COALESCE(link_type,'')
	              FROM ` + quoteIdent(s.schema) + `.memory_links WHERE bank_id = $1`
	edgeRows, err := s.db.QueryContext(ctx, edgeQuery, bankID)
	if err != nil {
		return GraphData{}, err
	}
	defer func() { _ = edgeRows.Close() }()
	edges := make([]GraphEdgeData, 0)
	for edgeRows.Next() {
		var edge GraphEdgeData
		var edgeBank string
		if err := edgeRows.Scan(&edge.ID, &edgeBank, &edge.From, &edge.To, &edge.Type); err != nil {
			return GraphData{}, err
		}
		if !kept[edge.From] || !kept[edge.To] {
			continue
		}
		edges = append(edges, edge)
	}
	return GraphData{Nodes: nodes, Edges: edges, TableRows: tableRows, Total: total, Limit: limit}, edgeRows.Err()
}

// EntityGraph returns the bank's entity graph.
func (s *PostgresStore) EntityGraph(ctx context.Context, bankID string, limit int) (EntityGraph, error) {
	limit = clampLimit(limit, 200)
	var totalEntities int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+quoteIdent(s.schema)+`.entities WHERE bank_id = $1`,
		bankID).Scan(&totalEntities); err != nil {
		return EntityGraph{}, err
	}
	nodeQuery := `SELECT id, COALESCE(name,'') FROM ` + quoteIdent(s.schema) + `.entities
	              WHERE bank_id = $1 ORDER BY mention_count DESC, id ASC LIMIT $2`
	rows, err := s.db.QueryContext(ctx, nodeQuery, bankID, limit)
	if err != nil {
		return EntityGraph{}, err
	}
	defer func() { _ = rows.Close() }()
	nodes := make([]EntityGraphNode, 0)
	kept := map[string]bool{}
	for rows.Next() {
		var node EntityGraphNode
		if err := rows.Scan(&node.ID, &node.Label); err != nil {
			return EntityGraph{}, err
		}
		kept[node.ID] = true
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return EntityGraph{}, err
	}

	edgeQuery := `SELECT COALESCE(entity_a,''), COALESCE(entity_b,''), COALESCE(count,0)
	              FROM ` + quoteIdent(s.schema) + `.entity_cooccurrences WHERE bank_id = $1
	              ORDER BY entity_a ASC, entity_b ASC`
	edgeRows, err := s.db.QueryContext(ctx, edgeQuery, bankID)
	if err != nil {
		return EntityGraph{}, err
	}
	defer func() { _ = edgeRows.Close() }()
	edges := make([]EntityGraphEdge, 0)
	for edgeRows.Next() {
		var source, target string
		var weight int
		if err := edgeRows.Scan(&source, &target, &weight); err != nil {
			return EntityGraph{}, err
		}
		if !kept[source] || !kept[target] {
			continue
		}
		edges = append(edges, EntityGraphEdge{
			ID: source + "->" + target, Source: source, Target: target, Weight: weight,
		})
	}
	return EntityGraph{Nodes: nodes, Edges: edges, TotalEntities: totalEntities,
		TotalEdges: len(edges), Limit: limit}, edgeRows.Err()
}

// scanDocumentRow reads the document projection.
func scanDocumentRow(row rowScanner) (Document, error) {
	var doc Document
	var metadata, retainParams, tagsRaw []byte
	if err := row.Scan(&doc.ID, &doc.BankID, &doc.OriginalText, &doc.ContentHash,
		&metadata, &retainParams, &tagsRaw, &doc.CreatedAt, &doc.UpdatedAt,
		&doc.MemoryUnitCount); err != nil {
		return Document{}, err
	}
	doc.Metadata = decodeJSONMap(metadata)
	doc.RetainParams = decodeJSONMap(retainParams)
	doc.Tags = decodePostgresTextArray(tagsRaw)
	return doc, nil
}

// scanChunkRow reads the chunk projection.
func scanChunkRow(row rowScanner) (Chunk, error) {
	var chunk Chunk
	if err := row.Scan(&chunk.ID, &chunk.BankID, &chunk.DocumentID, &chunk.Index,
		&chunk.Text, &chunk.ContentHash, &chunk.CreatedAt); err != nil {
		return Chunk{}, err
	}
	return chunk, nil
}

// scanEntityRow reads the entity projection.
func scanEntityRow(row rowScanner) (Entity, error) {
	var entity Entity
	var kind string
	var metadata []byte
	var firstSeen, lastSeen sql.NullTime
	if err := row.Scan(&entity.ID, &entity.BankID, &entity.Name, &kind,
		&metadata, &firstSeen, &lastSeen, &entity.MentionCount); err != nil {
		return Entity{}, err
	}
	entity.Kind = EntityKind(kind)
	entity.Metadata = decodeJSONMap(metadata)
	entity.FirstSeen = nullTimePtr(firstSeen)
	entity.LastSeen = nullTimePtr(lastSeen)
	return entity, nil
}

// nullableTagArray lets a tag filter be omitted, because an empty array would
// otherwise match only documents carrying no tags.
func nullableTagArray(tags []string) any {
	if len(tags) == 0 {
		return nil
	}
	return tags
}
