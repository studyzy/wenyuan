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
// Observations carry no document id of their own, so they are counted through
// the units the document owns: an observation belongs to the document when one
// of the document's units is among its sources.
func (s *PostgresStore) DocumentFactCounts(ctx context.Context, bankID, docID string) (map[string]int, error) {
	qualified := quoteIdent(s.schema)
	rows, err := s.reader().QueryContext(ctx,
		`SELECT COALESCE(fact_type,''), COUNT(*) FROM `+qualified+`.memory_units
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var observations int
	if err := s.reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.memory_units
		 WHERE bank_id = $1 AND fact_type = 'observation'
		   AND source_memory_ids && (SELECT array_agg(id) FROM `+qualified+`.memory_units
		                             WHERE bank_id = $1 AND document_id = $2)`,
		bankID, docID).Scan(&observations); err != nil {
		return nil, err
	}
	counts["observation"] = observations
	return counts, nil
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
	limit := query.Limit
	if limit < 0 {
		limit = 100
	}
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAnyStrict
	}
	qualified := quoteIdent(s.schema)
	args := []any{query.BankID}
	conds := []string{"bank_id = $1"}
	if term := query.Query; term != "" {
		args = append(args, "%"+term+"%")
		conds = append(conds, fmt.Sprintf("id ILIKE $%d", len(args)))
	}
	// The tag filter is appended as a literal clause, the way the graph read
	// builds it: the include-untagged modes need a disjunction a bound array
	// alone cannot express.
	if tagFilterActive(query.Tags, mode, query.TagGroups) {
		if len(query.TagGroups) > 0 {
			if clause, groupArgs := renderTagGroups(query.TagGroups, len(args)); clause != "" {
				args = append(args, groupArgs...)
				conds = append(conds, "("+clause+")")
			}
		} else {
			conds = append(conds, flatTagClause(query.Tags, mode))
		}
	}
	// The time window's axis drives both the filter and the ordering, and rows
	// with no value on that axis are excluded once any bound or axis is given.
	orderBy := "updated_at DESC, created_at DESC, id ASC"
	if query.TimeField != "" || query.TimeStart != nil || query.TimeEnd != nil {
		column := "updated_at"
		if query.TimeField == "created_at" {
			column = "created_at"
		}
		conds = append(conds, column+" IS NOT NULL")
		if query.TimeStart != nil {
			args = append(args, *query.TimeStart)
			conds = append(conds, fmt.Sprintf("%s >= $%d", column, len(args)))
		}
		if query.TimeEnd != nil {
			args = append(args, *query.TimeEnd)
			conds = append(conds, fmt.Sprintf("%s < $%d", column, len(args)))
		}
		orderBy = column + " DESC, id ASC"
	}
	filter := strings.Join(conds, " AND ")
	limitParam := "$" + itoa(len(args)+1)
	offsetParam := "$" + itoa(len(args)+2)
	var total int
	countQuery := `SELECT COUNT(*) FROM ` + qualified + `.documents WHERE ` + filter
	if err := s.reader().QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return DocumentPage{}, err
	}
	listQuery := `SELECT id, bank_id, COALESCE(original_text,''), COALESCE(content_hash,''),
	                     COALESCE(retain_params, '{}'::jsonb),
	                     COALESCE(tags, '{}'), created_at, updated_at,
	                     (SELECT COUNT(*) FROM ` + qualified + `.memory_units u
	                      WHERE u.bank_id = d.bank_id AND u.document_id = d.id)
	              FROM ` + qualified + `.documents d
	              WHERE ` + filter + `
	              ORDER BY ` + orderBy + ` LIMIT ` + limitParam + ` OFFSET ` + offsetParam
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
	// The route already bounds the page size (1..1000), and the reference hands
	// it straight to LIMIT, so it is not capped again here: a cap below the
	// route's own bound would silently return a short page.
	limit := query.Limit
	if limit < 0 {
		limit = 100
	}
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

// FindChunk returns one stored chunk by identifier, wherever it is stored. The
// identifier is the chunks table's key, so no bank filter applies.
func (s *PostgresStore) FindChunk(ctx context.Context, chunkID string) (Chunk, error) {
	query := `SELECT chunk_id, bank_id, COALESCE(document_id,''), COALESCE(chunk_index,0), COALESCE(chunk_text,''),
	                 COALESCE(content_hash,''), created_at
	          FROM ` + quoteIdent(s.schema) + `.chunks WHERE chunk_id = $1`
	chunk, err := scanChunkRow(s.reader().QueryRowContext(ctx, query, chunkID))
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
// recomputes each entity's mention count and dates over those memories only;
// without one the stored entity columns are served as they stand.
func (s *PostgresStore) ListEntities(ctx context.Context, query EntityQuery) (EntityPage, error) {
	// The route bounds only the lower end (`ge=0`), so the caller's page size is
	// handed to LIMIT unchanged — the reference does the same.
	limit := query.Limit
	if limit < 0 {
		limit = 100
	}
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAny
	}
	qualified := quoteIdent(s.schema)
	args := []any{query.BankID}
	// Without a tag filter the entity's stored columns ARE the answer: the
	// reference serves `entities.mention_count / first_seen / last_seen` as they
	// stand (list_entities and get_entity_detail both read the row), and those
	// stamps cover every memory that ever named the entity, so they can exceed
	// the live mentioning set. A tag filter is what forces a recomputation: the
	// counts and dates must cover only the memories the caller's scope admits,
	// and an entity no matching memory mentions has no row at all.
	mentionExpr, firstExpr, lastExpr := "COALESCE(e.mention_count,0)", "e.first_seen", "e.last_seen"
	from := qualified + `.entities e`
	if tagFilterActive(query.Tags, mode, query.TagGroups) {
		rendered, tagArgs := entityTagPredicate(query.Tags, mode, query.TagGroups, len(args))
		args = append(args, tagArgs...)
		mentionExpr, firstExpr, lastExpr = "s.mention_count", "s.first_seen", "s.last_seen"
		from = qualified + `.entities e JOIN (
	    SELECT ue.entity_id,
	           COUNT(*) AS mention_count,
	           MIN(vu.seen_at) AS first_seen,
	           MAX(vu.seen_at) AS last_seen
	    FROM ` + qualified + `.unit_entities ue
	    JOIN (
	        SELECT id, COALESCE(occurred_start, mentioned_at, event_date) AS seen_at
	        FROM ` + qualified + `.memory_units
	        WHERE bank_id = $1 AND (` + rendered + `)
	    ) vu ON vu.id = ue.unit_id
	    GROUP BY ue.entity_id
	) s ON s.entity_id = e.id`
	}
	where := `WHERE e.bank_id = $1`
	if query.ID != "" {
		args = append(args, query.ID)
		where += fmt.Sprintf(" AND e.id = $%d::uuid", len(args))
	}
	orderExpr := mentionExpr + ` DESC, ` + lastExpr + ` DESC, e.id ASC`
	var total int
	if err := s.reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+from+` `+where, args...).Scan(&total); err != nil {
		return EntityPage{}, err
	}
	pageArgs := append(append([]any{}, args...), limit, query.Offset)
	listSQL := `SELECT e.id, e.bank_id, COALESCE(e.canonical_name,''), COALESCE(e.entity_kind,''),
	                   COALESCE(e.metadata, '{}'::jsonb), ` + firstExpr + `, ` + lastExpr + `, ` + mentionExpr + `
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
	if term := query.Query; term != "" {
		args = append(args, "%"+term+"%")
		conds = append(conds, fmt.Sprintf("(text ILIKE $%d OR context ILIKE $%d)", len(args), len(args)))
	}
	if query.DocumentID != "" {
		args = append(args, query.DocumentID)
		conds = append(conds, fmt.Sprintf(
			"(document_id = $%d OR (fact_type = 'observation' AND source_memory_ids && "+
				"(SELECT array_agg(id) FROM %s WHERE bank_id = $1 AND document_id = $%d)))",
			len(args), qualified+".memory_units", len(args)))
	}
	if query.ChunkID != "" {
		args = append(args, query.ChunkID)
		conds = append(conds, fmt.Sprintf(
			"(chunk_id = $%d OR (fact_type = 'observation' AND source_memory_ids && "+
				"(SELECT array_agg(id) FROM %s WHERE bank_id = $1 AND chunk_id = $%d)))",
			len(args), qualified+".memory_units", len(args)))
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
	                     COALESCE(document_id,''), COALESCE(chunk_id,''), COALESCE(tags, '{}'),
	                     created_at, COALESCE(proof_count, 0), COALESCE(source_memory_ids, '{}')
	              FROM ` + qualified + `.memory_units ` + where + `
	              ORDER BY mentioned_at DESC NULLS LAST, event_date DESC, id ASC LIMIT $` + itoa(len(args)+1)
	rows, err := s.reader().QueryContext(ctx, unitQuery, pageArgs...)
	if err != nil {
		return GraphData{}, err
	}
	defer func() { _ = rows.Close() }()

	nodes := make([]GraphNodeData, 0)
	tableRows := make([]GraphTableRow, 0)
	for rows.Next() {
		var row GraphTableRow
		var occurredStart, occurredEnd, mentionedAt, eventDate, createdAt sql.NullTime
		var tagsRaw, sourcesRaw []byte
		if err := rows.Scan(&row.ID, &row.Text, &row.FactType, &row.Context,
			&occurredStart, &occurredEnd, &mentionedAt, &eventDate,
			&row.DocumentID, &row.ChunkID, &tagsRaw,
			&createdAt, &row.ProofCount, &sourcesRaw); err != nil {
			return GraphData{}, err
		}
		row.OccurredStart = nullTimePtr(occurredStart)
		row.OccurredEnd = nullTimePtr(occurredEnd)
		row.MentionedAt = nullTimePtr(mentionedAt)
		row.Date = nullTimePtr(eventDate)
		row.CreatedAt = nullTimePtr(createdAt)
		row.Tags = decodePostgresTextArray(tagsRaw)
		tableRows = append(tableRows, row)
		nodes = append(nodes, GraphNodeData{
			ID: row.ID, Text: row.Text, Context: row.Context, FactType: row.FactType,
			DocumentID: row.DocumentID, ChunkID: row.ChunkID, Tags: row.Tags,
			Mentioned: row.MentionedAt, Date: row.Date, CreatedAt: row.CreatedAt,
			SourceMemoryIDs: decodePostgresTextArray(sourcesRaw),
		})
	}
	if err := rows.Err(); err != nil {
		return GraphData{}, err
	}

	// Entities are postings rather than columns, so the page's entities are
	// fetched in one lookup instead of one query per node. The lookup also
	// inherits an observation's entities from its source memories.
	unitIDs := make([]string, 0, len(tableRows))
	for _, row := range tableRows {
		unitIDs = append(unitIDs, row.ID)
	}
	entityNames, err := s.UnitEntityNames(ctx, query.BankID, unitIDs)
	if err != nil {
		return GraphData{}, err
	}
	for i := range tableRows {
		names := entityNames[tableRows[i].ID]
		tableRows[i].Entities = strings.Join(names, ", ")
		nodes[i].Entities = names
	}

	// The links of the visible units and of the source memories they inherit
	// from, strongest first and bounded, so the derived edges can be built from
	// one read.
	linkIDs := make([]string, 0, len(unitIDs))
	linkIDs = append(linkIDs, unitIDs...)
	for _, node := range nodes {
		linkIDs = append(linkIDs, node.SourceMemoryIDs...)
	}
	links := make([]GraphEdgeData, 0)
	if len(linkIDs) > 0 {
		edgeQuery := `SELECT from_unit_id::text, to_unit_id::text, COALESCE(link_type,''), COALESCE(weight, 0)
		              FROM ` + qualified + `.memory_links
		              WHERE from_unit_id = ANY($1::uuid[]) AND to_unit_id = ANY($1::uuid[])
		              ORDER BY weight DESC NULLS LAST LIMIT $2`
		edgeRows, err := s.reader().QueryContext(ctx, edgeQuery, linkIDs, graphEdgeCap)
		if err != nil {
			return GraphData{}, err
		}
		defer func() { _ = edgeRows.Close() }()
		for edgeRows.Next() {
			var edge GraphEdgeData
			if err := edgeRows.Scan(&edge.From, &edge.To, &edge.Type, &edge.Weight); err != nil {
				return GraphData{}, err
			}
			links = append(links, edge)
		}
		if err := edgeRows.Err(); err != nil {
			return GraphData{}, err
		}
	}
	edges := deriveGraphEdges(nodes, entityNames, links)
	return GraphData{Nodes: nodes, Edges: edges, TableRows: tableRows, Total: total, Limit: limit}, nil
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
	mode := query.TagsMatch
	if mode == "" {
		mode = TagsMatchAny
	}
	qualified := quoteIdent(s.schema)
	bankID := query.BankID

	type edgeScan struct {
		from, to, name1, name2 string
		weight, mc1, mc2       int
		lastCooccurred         sql.NullTime
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
		    SELECT ue.unit_id, ue.entity_id, COALESCE(mu.occurred_start, mu.mentioned_at, mu.event_date) AS seen_at
		    FROM ` + qualified + `.unit_entities ue
		    JOIN ` + qualified + `.memory_units mu ON mu.id = ue.unit_id
		    WHERE mu.bank_id = $1 AND (` + tagSQL + `)
		), pairs AS (
		    SELECT a.entity_id AS id1, b.entity_id AS id2, COUNT(*) AS w, MAX(a.seen_at) AS last_cooccurred
		    FROM ve a JOIN ve b ON b.unit_id = a.unit_id AND a.entity_id < b.entity_id
		    GROUP BY a.entity_id, b.entity_id
		    HAVING COUNT(*) >= $` + minParam + `
		), stats AS (
		    SELECT entity_id, COUNT(*) AS mc FROM ve GROUP BY entity_id
		)
		SELECT p.id1::text, p.id2::text, p.w, e1.canonical_name, s1.mc, e2.canonical_name, s2.mc, p.last_cooccurred
		FROM pairs p
		JOIN ` + qualified + `.entities e1 ON e1.id = p.id1
		JOIN ` + qualified + `.entities e2 ON e2.id = p.id2
		JOIN stats s1 ON s1.entity_id = p.id1
		JOIN stats s2 ON s2.entity_id = p.id2
		WHERE e1.bank_id = $1 AND e2.bank_id = $1
		ORDER BY p.w DESC, p.last_cooccurred DESC, p.id1, p.id2
		LIMIT $` + limitParam
		rows, err = s.reader().QueryContext(ctx, edgeQuery, args...)
	} else {
		edgeQuery := `SELECT ec.entity_id_1::text, ec.entity_id_2::text, COALESCE(ec.cooccurrence_count,0),
		                     e1.canonical_name, COALESCE(e1.mention_count,0),
		                     e2.canonical_name, COALESCE(e2.mention_count,0), ec.last_cooccurred
		              FROM ` + qualified + `.entity_cooccurrences ec
		              JOIN ` + qualified + `.entities e1 ON e1.id = ec.entity_id_1
		              JOIN ` + qualified + `.entities e2 ON e2.id = ec.entity_id_2
		              WHERE e1.bank_id = $1 AND e2.bank_id = $1 AND ec.cooccurrence_count >= $2
		              ORDER BY ec.cooccurrence_count DESC, ec.last_cooccurred DESC, ec.entity_id_1, ec.entity_id_2
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
		if err := rows.Scan(&sc.from, &sc.to, &sc.weight, &sc.name1, &sc.mc1, &sc.name2, &sc.mc2, &sc.lastCooccurred); err != nil {
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
		edges = append(edges, EntityGraphEdge{
			ID: sc.from + "->" + sc.to, Source: sc.from, Target: sc.to, Weight: sc.weight,
			LastCooccurred: nullTimePtr(sc.lastCooccurred),
		})
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
	doc.Metadata = DocumentMetadata(doc.RetainParams)
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
