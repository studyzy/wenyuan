package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// clampLimit applies the shared page bound. A negative limit selects the
// fallback; zero is a valid explicit page size that returns nothing; anything
// above the hard cap is capped so a caller cannot ask for an unbounded read.
func clampLimit(limit, fallback int) int {
	if limit < 0 {
		return fallback
	}
	if limit > 500 {
		return 500
	}
	return limit
}

// DocumentFactCounts returns the document's memory units grouped by fact type.
func (s *PostgresStore) DocumentFactCounts(ctx context.Context, bankID, docID string) (map[string]int, error) {
	rows, err := s.reader().QueryContext(ctx,
		`SELECT COALESCE(fact_type,''), COUNT(*) FROM `+quoteIdent(s.schema)+`.memory_units
		 WHERE bank_id = $1 AND document_id = $2 GROUP BY fact_type`, bankID, docID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int{}
	for rows.Next() {
		var factType string
		var count int
		if err := rows.Scan(&factType, &count); err != nil {
			return nil, err
		}
		counts[factType] = count
	}
	return counts, rows.Err()
}

// EntityMentions returns the memory texts that mention an entity.
func (s *PostgresStore) EntityMentions(ctx context.Context, bankID, entityID string, limit int) ([]EntityMention, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT COALESCE(u.text,''), u.mentioned_at
		 FROM `+quoteIdent(s.schema)+`.unit_entities ue
		 JOIN `+quoteIdent(s.schema)+`.memory_units u ON u.id = ue.unit_id
		 WHERE ue.entity_id = $1::uuid AND u.bank_id = $2
		 LIMIT $3`, entityID, bankID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	mentions := make([]EntityMention, 0)
	for rows.Next() {
		var mention EntityMention
		var mentionedAt sql.NullTime
		if err := rows.Scan(&mention.Text, &mentionedAt); err != nil {
			return nil, err
		}
		mention.MentionedAt = nullTimePtr(mentionedAt)
		mentions = append(mentions, mention)
	}
	return mentions, rows.Err()
}

// UnitEntities returns the canonical names of the entities one memory unit
// mentions, in name order.
func (s *PostgresStore) UnitEntities(ctx context.Context, bankID, unitID string) ([]string, error) {
	query := `SELECT COALESCE(e.canonical_name,'')
	          FROM ` + quoteIdent(s.schema) + `.unit_entities ue
	          JOIN ` + quoteIdent(s.schema) + `.entities e ON e.id = ue.entity_id
	          WHERE ue.unit_id = $1::uuid AND e.bank_id = $2
	          ORDER BY e.canonical_name ASC`
	rows, err := s.reader().QueryContext(ctx, query, unitID, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// ListDocuments returns one page of a bank's documents plus the total.
func (s *PostgresStore) ListDocuments(ctx context.Context, query DocumentQuery) (DocumentPage, error) {
	limit := clampLimit(query.Limit, 100)
	tagExpr := `tags::text[] && $3`
	switch query.TagsMatch {
	case TagsMatchAll, TagsMatchAllStrict:
		tagExpr = `tags::text[] @> $3`
	case TagsMatchExact:
		tagExpr = `(tags::text[] <@ $3 AND tags::text[] @> $3)`
	}
	timeColumn := "created_at"
	if query.TimeField == "updated_at" {
		timeColumn = "updated_at"
	}
	args := []any{query.BankID, strings.TrimSpace(query.Query), nullableTagArray(query.Tags),
		query.TimeStart, query.TimeEnd}
	filter := `bank_id = $1
	           AND ($2 = '' OR id ILIKE '%' || $2 || '%')
	           AND ($3::text[] IS NULL OR ` + tagExpr + `)
	           AND ($4::timestamptz IS NULL OR ` + timeColumn + ` >= $4)
	           AND ($5::timestamptz IS NULL OR ` + timeColumn + ` <= $5)`
	// A compound filter — the caller's resolved read scope — is AND-ed in as an
	// extra predicate, so a scoped reader narrows the listing without replacing
	// the request's own tag filter. The placeholders continue past the fixed
	// ones, so LIMIT/OFFSET are addressed after whatever it added.
	if groupClause, groupArgs := renderTagGroups(query.TagGroups, len(args)); groupClause != "" {
		filter += ` AND (` + groupClause + `)`
		args = append(args, groupArgs...)
	}
	limitParam := "$" + itoa(len(args)+1)
	offsetParam := "$" + itoa(len(args)+2)
	var total int
	countQuery := `SELECT COUNT(*) FROM ` + quoteIdent(s.schema) + `.documents WHERE ` + filter
	if err := s.reader().QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return DocumentPage{}, err
	}
	listQuery := `SELECT id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''),
	                     COALESCE(retain_params, '{}'::jsonb),
	                     COALESCE(tags, '{}'), created_at, updated_at,
	                     (SELECT COUNT(*) FROM ` + quoteIdent(s.schema) + `.memory_units u
	                      WHERE u.bank_id = d.bank_id AND u.document_id = d.id)
	              FROM ` + quoteIdent(s.schema) + `.documents d
	              WHERE ` + filter + `
	              ORDER BY created_at DESC, id ASC LIMIT ` + limitParam + ` OFFSET ` + offsetParam
	listArgs := append(append([]any{}, args...), limit, query.Offset)
	rows, err := s.reader().QueryContext(ctx, listQuery, listArgs...)
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
	if err := s.reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+quoteIdent(s.schema)+`.chunks WHERE `+filter,
		query.BankID, query.DocumentID).Scan(&total); err != nil {
		return ChunkPage{}, err
	}
	listQuery := `SELECT chunk_id, bank_id, COALESCE(document_id,''), COALESCE(chunk_index,0), COALESCE(chunk_text,''),
	                     COALESCE(content_hash,''), created_at
	              FROM ` + quoteIdent(s.schema) + `.chunks
	              WHERE ` + filter + `
	              ORDER BY document_id ASC, chunk_index ASC LIMIT $3 OFFSET $4`
	rows, err := s.reader().QueryContext(ctx, listQuery, query.BankID, query.DocumentID, limit, query.Offset)
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
	query := `SELECT chunk_id, bank_id, COALESCE(document_id,''), COALESCE(chunk_index,0), COALESCE(chunk_text,''),
	                 COALESCE(content_hash,''), created_at
	          FROM ` + quoteIdent(s.schema) + `.chunks WHERE bank_id = $1 AND chunk_id = $2`
	chunk, err := scanChunkRow(s.reader().QueryRowContext(ctx, query, bankID, chunkID))
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
	              FROM ` + quoteIdent(s.schema) + `.memory_units
	              WHERE bank_id = $1 AND tags IS NOT NULL AND tags != '{}'
	          ) AS expanded
	          GROUP BY tag ORDER BY COUNT(*) DESC, tag ASC`
	rows, err := s.reader().QueryContext(ctx, query, bankID)
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

// ListEntities returns one page of a bank's entities plus the total. A tag
// filter restricts the listing to entities a matching memory mentions and
// recomputes each entity's mention count and dates over those memories only.
func (s *PostgresStore) ListEntities(ctx context.Context, query EntityQuery) (EntityPage, error) {
	limit := clampLimit(query.Limit, 100)
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAny
	}
	qualified := quoteIdent(s.schema)
	args := []any{query.BankID}
	// The mention count is the number of memories that name the entity NOW, so
	// it is counted live from unit_entities rather than read from the cached
	// column: a replaced or deleted document's mentions must not keep counting.
	// The entity's own first_seen/last_seen stamps are still the entity's.
	tagSQL := "TRUE"
	filtered := tagFilterActive(query.Tags, mode, query.TagGroups)
	if filtered {
		rendered, tagArgs := entityTagPredicate(query.Tags, mode, query.TagGroups, len(args))
		args = append(args, tagArgs...)
		tagSQL = rendered
	}
	// Without a filter this is a LEFT JOIN, so an entity whose mentions have all
	// gone still appears — with a count of zero, which is what "mentions it now"
	// means — rather than vanishing the moment its last document is replaced.
	// Under a tag filter it is an inner join: a scoped reader must not learn that
	// an entity only another scope names exists, so an entity with no in-scope
	// mention is not listed at all (and reads as not-found by id).
	join := "LEFT JOIN"
	if filtered {
		join = "JOIN"
	}
	from := qualified + `.entities e ` + join + ` (
		    SELECT ue.entity_id, COUNT(*) AS mention_count
		    FROM ` + qualified + `.unit_entities ue
		    JOIN ` + qualified + `.memory_units mu ON mu.id = ue.unit_id
		    WHERE mu.bank_id = $1 AND (` + tagSQL + `)
		    GROUP BY ue.entity_id
		) st ON st.entity_id = e.id`
	mentionExpr := `COALESCE(st.mention_count, 0)`
	orderExpr := `COALESCE(st.mention_count, 0) DESC, e.id ASC`
	where := `WHERE e.bank_id = $1`
	if query.ID != "" {
		args = append(args, query.ID)
		where += fmt.Sprintf(" AND e.id = $%d::uuid", len(args))
	}
	var total int
	if err := s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+from+` `+where, args...).Scan(&total); err != nil {
		return EntityPage{}, err
	}
	pageArgs := append(append([]any{}, args...), limit, query.Offset)
	listSQL := `SELECT e.id, e.bank_id, COALESCE(e.canonical_name,''), COALESCE(e.entity_kind,''),
	                   COALESCE(e.metadata, '{}'::jsonb), e.first_seen, e.last_seen, ` + mentionExpr + `
	            FROM ` + from + ` ` + where + `
	            ORDER BY ` + orderExpr + ` LIMIT $` + itoa(len(args)+1) + ` OFFSET $` + itoa(len(args)+2)
	rows, err := s.reader().QueryContext(ctx, listSQL, pageArgs...)
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

// entityTagPredicate renders the tag predicate for a memory_units scope whose
// tags column is addressed as the bare column `tags`, returning SQL plus any
// parameters it introduces. bound is the number of parameters already placed in
// the statement, so the predicate's first new placeholder is bound+1.
func entityTagPredicate(tags []string, mode string, groups []TagGroup, bound int) (string, []any) {
	if len(groups) > 0 {
		sql, args := renderTagGroups(groups, bound)
		if sql == "" {
			return "TRUE", nil
		}
		return sql, args
	}
	return flatTagClause(tags, mode), nil
}

// Graph returns the bank's memory graph for the query's filtered unit set.
func (s *PostgresStore) Graph(ctx context.Context, query GraphQuery) (GraphData, error) {
	limit := query.Limit
	if limit < 0 {
		limit = 1000
	}
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAllStrict
	}
	qualified := quoteIdent(s.schema)
	args := []any{query.BankID}
	conds := []string{"bank_id = $1"}
	if query.Type != "" {
		args = append(args, query.Type)
		conds = append(conds, fmt.Sprintf("fact_type = $%d", len(args)))
	}
	if term := strings.TrimSpace(query.Query); term != "" {
		args = append(args, "%"+term+"%")
		conds = append(conds, fmt.Sprintf("(text ILIKE $%d OR context ILIKE $%d)", len(args), len(args)))
	}
	if query.DocumentID != "" {
		args = append(args, query.DocumentID)
		conds = append(conds, fmt.Sprintf("document_id = $%d", len(args)))
	}
	if query.ChunkID != "" {
		args = append(args, query.ChunkID)
		conds = append(conds, fmt.Sprintf("chunk_id = $%d", len(args)))
	}
	if tagFilterActive(query.Tags, mode, query.TagGroups) {
		tagSQL, tagArgs := entityTagPredicate(query.Tags, mode, query.TagGroups, len(args))
		args = append(args, tagArgs...)
		conds = append(conds, "("+tagSQL+")")
	}
	where := "WHERE " + strings.Join(conds, " AND ")

	var total int
	if err := s.reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.memory_units `+where, args...).Scan(&total); err != nil {
		return GraphData{}, err
	}
	pageArgs := append(append([]any{}, args...), limit)
	unitQuery := `SELECT id::text, COALESCE(text,''), COALESCE(fact_type,''), COALESCE(context,''),
	                     occurred_start, occurred_end, mentioned_at, event_date,
	                     COALESCE(document_id,''), COALESCE(chunk_id,''), COALESCE(tags, '{}')
	              FROM ` + qualified + `.memory_units ` + where + `
	              ORDER BY mentioned_at DESC NULLS LAST, id ASC LIMIT $` + itoa(len(args)+1)
	rows, err := s.reader().QueryContext(ctx, unitQuery, pageArgs...)
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
			ID: row.ID, Text: row.Text, Context: row.Context, FactType: row.FactType,
			DocumentID: row.DocumentID, ChunkID: row.ChunkID, Tags: row.Tags,
			Mentioned: row.MentionedAt, Date: row.Date,
		})
	}
	if err := rows.Err(); err != nil {
		return GraphData{}, err
	}

	// Entities are postings rather than columns, so the page's entities are
	// fetched in one lookup instead of one query per node.
	unitIDs := make([]string, 0, len(tableRows))
	for _, row := range tableRows {
		unitIDs = append(unitIDs, row.ID)
	}
	entityNames, err := s.UnitEntityNames(ctx, query.BankID, unitIDs)
	if err != nil {
		return GraphData{}, err
	}
	for i := range tableRows {
		names, ok := entityNames[tableRows[i].ID]
		if !ok {
			continue
		}
		tableRows[i].Entities = strings.Join(names, ", ")
		nodes[i].Entities = names
	}

	// An edge is kept only when both endpoints are inside the bounded node set.
	edgeQuery := `SELECT bank_id, from_unit_id::text, to_unit_id::text, COALESCE(link_type,'')
	              FROM ` + qualified + `.memory_links WHERE bank_id = $1`
	edgeRows, err := s.reader().QueryContext(ctx, edgeQuery, query.BankID)
	if err != nil {
		return GraphData{}, err
	}
	defer func() { _ = edgeRows.Close() }()
	edges := make([]GraphEdgeData, 0)
	for edgeRows.Next() {
		var edge GraphEdgeData
		var edgeBank string
		if err := edgeRows.Scan(&edgeBank, &edge.From, &edge.To, &edge.Type); err != nil {
			return GraphData{}, err
		}
		if !kept[edge.From] || !kept[edge.To] {
			continue
		}
		edge.ID = edge.From + "->" + edge.To
		edges = append(edges, edge)
	}
	return GraphData{Nodes: nodes, Edges: edges, TableRows: tableRows, Total: total, Limit: limit}, edgeRows.Err()
}

// EntityGraph returns the bank's entity co-occurrence graph. Nodes are the
// endpoints of the returned edges; a tag filter recomputes mention counts and
// edges from the matching memories only.
func (s *PostgresStore) EntityGraph(ctx context.Context, query EntityGraphQuery) (EntityGraph, error) {
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
	qualified := quoteIdent(s.schema)
	bankID := query.BankID

	type edgeScan struct {
		from, to, name1, name2 string
		weight, mc1, mc2       int
	}
	var (
		rows *sql.Rows
		err  error
	)
	if tagFilterActive(query.Tags, mode, query.TagGroups) {
		tagSQL, tagArgs := entityTagPredicate(query.Tags, mode, query.TagGroups, 1)
		args := append([]any{bankID}, tagArgs...)
		args = append(args, minCount, limit)
		minParam := itoa(len(args) - 1)
		limitParam := itoa(len(args))
		edgeQuery := `WITH ve AS (
		    SELECT ue.unit_id, ue.entity_id
		    FROM ` + qualified + `.unit_entities ue
		    JOIN ` + qualified + `.memory_units mu ON mu.id = ue.unit_id
		    WHERE mu.bank_id = $1 AND (` + tagSQL + `)
		), pairs AS (
		    SELECT a.entity_id AS id1, b.entity_id AS id2, COUNT(*) AS w
		    FROM ve a JOIN ve b ON b.unit_id = a.unit_id AND a.entity_id < b.entity_id
		    GROUP BY a.entity_id, b.entity_id
		    HAVING COUNT(*) >= $` + minParam + `
		), stats AS (
		    SELECT entity_id, COUNT(*) AS mc FROM ve GROUP BY entity_id
		)
		SELECT p.id1::text, p.id2::text, p.w, e1.canonical_name, s1.mc, e2.canonical_name, s2.mc
		FROM pairs p
		JOIN ` + qualified + `.entities e1 ON e1.id = p.id1
		JOIN ` + qualified + `.entities e2 ON e2.id = p.id2
		JOIN stats s1 ON s1.entity_id = p.id1
		JOIN stats s2 ON s2.entity_id = p.id2
		WHERE e1.bank_id = $1 AND e2.bank_id = $1
		ORDER BY p.w DESC, p.id1 ASC, p.id2 ASC
		LIMIT $` + limitParam
		rows, err = s.reader().QueryContext(ctx, edgeQuery, args...)
	} else {
		edgeQuery := `SELECT ec.entity_id_1::text, ec.entity_id_2::text, COALESCE(ec.cooccurrence_count,0),
		                     e1.canonical_name, COALESCE(e1.mention_count,0),
		                     e2.canonical_name, COALESCE(e2.mention_count,0)
		              FROM ` + qualified + `.entity_cooccurrences ec
		              JOIN ` + qualified + `.entities e1 ON e1.id = ec.entity_id_1
		              JOIN ` + qualified + `.entities e2 ON e2.id = ec.entity_id_2
		              WHERE e1.bank_id = $1 AND e2.bank_id = $1 AND ec.cooccurrence_count >= $2
		              ORDER BY ec.cooccurrence_count DESC, ec.entity_id_1 ASC, ec.entity_id_2 ASC
		              LIMIT $3`
		rows, err = s.reader().QueryContext(ctx, edgeQuery, bankID, minCount, limit)
	}
	if err != nil {
		return EntityGraph{}, err
	}
	defer func() { _ = rows.Close() }()
	scans := make([]edgeScan, 0)
	for rows.Next() {
		var sc edgeScan
		if err := rows.Scan(&sc.from, &sc.to, &sc.weight, &sc.name1, &sc.mc1, &sc.name2, &sc.mc2); err != nil {
			return EntityGraph{}, err
		}
		scans = append(scans, sc)
	}
	if err := rows.Err(); err != nil {
		return EntityGraph{}, err
	}

	index := map[string]int{}
	nodes := make([]EntityGraphNode, 0)
	edges := make([]EntityGraphEdge, 0, len(scans))
	addNode := func(id, label string, mentions int) {
		if _, ok := index[id]; !ok {
			index[id] = len(nodes)
			nodes = append(nodes, EntityGraphNode{ID: id, Label: label, MentionCount: mentions})
		}
	}
	for _, sc := range scans {
		addNode(sc.from, sc.name1, sc.mc1)
		addNode(sc.to, sc.name2, sc.mc2)
		edges = append(edges, EntityGraphEdge{ID: sc.from + "->" + sc.to, Source: sc.from, Target: sc.to, Weight: sc.weight})
	}
	return EntityGraph{Nodes: nodes, Edges: edges, TotalEntities: len(nodes),
		TotalEdges: len(edges), Limit: limit}, nil
}

// scanDocumentRow reads the document projection.
func scanDocumentRow(row rowScanner) (Document, error) {
	var doc Document
	var retainParams, tagsRaw []byte
	if err := row.Scan(&doc.ID, &doc.BankID, &doc.OriginalText, &doc.ContentHash,
		&retainParams, &tagsRaw, &doc.CreatedAt, &doc.UpdatedAt,
		&doc.MemoryUnitCount); err != nil {
		return Document{}, err
	}
	doc.RetainParams = decodeJSONMap(retainParams)
	doc.Metadata = documentMetadata(doc.RetainParams)
	doc.Tags = decodePostgresTextArray(tagsRaw)
	return doc, nil
}

// scanChunkRow reads the chunk projection.
func scanChunkRow(row rowScanner) (Chunk, error) {
	var chunk Chunk
	if err := row.Scan(&chunk.ChunkID, &chunk.BankID, &chunk.DocumentID, &chunk.ChunkIndex,
		&chunk.ChunkText, &chunk.ContentHash, &chunk.CreatedAt); err != nil {
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
	if err := row.Scan(&entity.ID, &entity.BankID, &entity.CanonicalName, &kind,
		&metadata, &firstSeen, &lastSeen, &entity.MentionCount); err != nil {
		return Entity{}, err
	}
	entity.EntityKind = EntityKind(kind)
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

// documentMetadata reads the document metadata the reference keeps inside
// retain_params. The document has no metadata column, and this series serves
// the value on the same wire key the reference does, so it is derived from the
// nested object rather than stored beside it.
func documentMetadata(retainParams map[string]any) map[string]any {
	if retainParams == nil {
		return nil
	}
	nested, ok := retainParams["metadata"].(map[string]any)
	if !ok {
		return nil
	}
	return nested
}
