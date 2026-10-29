package storage

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// knowledgeSelect is the shared projection for the knowledge tree. A row
// carries the name and the position; a page's text, tags and description live
// on the mental model the row points at, so the projection joins both and a
// caller reads one node rather than two rows.
const knowledgeSelect = `kp.id, kp.bank_id, COALESCE(kp.parent_id, ''), kp.kind, kp.name,
       COALESCE(kp.mental_model_id, ''), COALESCE(mm.content, ''), COALESCE(mm.tags, '{}')::text,
       COALESCE(kp.managed, false), COALESCE(kp.sort_order, 0),
       kp.created_at, kp.updated_at`

// knowledgeFrom is the shared source. The join is on the pair, not the model id
// alone, because a mental model identifier is unique within its bank.
func knowledgeFrom(schema string) string {
	return quoteIdent(schema) + `.knowledge_pages kp
	        LEFT JOIN ` + quoteIdent(schema) + `.mental_models mm
	               ON mm.id = kp.mental_model_id AND mm.bank_id = kp.bank_id`
}

// ListKnowledgeNodes returns every knowledge node of one bank, ordered the way
// the reference orders the flat tree: by sort_order then name, so siblings
// appear in display order and the nest needs no second sort.
//
// The name comparison runs here rather than in ORDER BY: the reference sorts the
// fetched rows in Python, whose string order is by code point, while a database
// collation orders case-insensitively ('a' before 'Alpha' where the reference
// puts 'Alpha' first). Both surfaces that read this — the tree and the export
// index — render that order, so it has to be the reference's.
func (s *PostgresStore) ListKnowledgeNodes(ctx context.Context, bankID string) ([]KnowledgeNode, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	query := `SELECT ` + knowledgeSelect + ` FROM ` + knowledgeFrom(schema) + `
	          WHERE kp.bank_id = $1
	          ORDER BY kp.sort_order, kp.id`
	rows, err := s.reader().QueryContext(ctx, query, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]KnowledgeNode, 0)
	for rows.Next() {
		node, err := scanKnowledgeNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The reference's key is `(sort_order is None, sort_order or 0, name or "")`
	// (_knowledge_tree_sort_key, engine/memory_engine.py:1945-1949) — two keys,
	// no identifier. Python's sorted() is stable, so a tie keeps the order the
	// rows were fetched in; adding an id third key here reordered siblings the
	// reference leaves alone, which showed up as a tree listed in `kf-<hex>` id
	// order instead of name order whenever the two disagreed.
	sort.SliceStable(out, func(i, j int) bool {
		return knowledgeTreeLess(out[i], out[j])
	})
	return out, nil
}

// knowledgeTreeLess is the reference's `_knowledge_tree_sort_key` comparison:
// sort_order first, then name. The stored sort_order is already coalesced to 0,
// so a row without one sorts with the zeroes exactly as the reference's
// `sort_order or 0` does.
//
// Python's sorted() is stable, so rows tying on both keys keep their fetched
// order; there is deliberately no identifier third key, because the reference
// has none.
func knowledgeTreeLess(left, right KnowledgeNode) bool {
	if left.SortOrder != right.SortOrder {
		return left.SortOrder < right.SortOrder
	}
	return left.Title < right.Title
}

// GetKnowledgeNode returns one knowledge node by identifier.
func (s *PostgresStore) GetKnowledgeNode(ctx context.Context, bankID, nodeID string) (KnowledgeNode, error) {
	return s.getKnowledgeNodeOn(ctx, s.reader(), bankID, nodeID)
}

// getKnowledgeNodeOn reads one node through the given connection, so a read
// serves both a standalone lookup and one inside a caller's transaction — the
// read the reference takes inside the patch's transaction.
func (s *PostgresStore) getKnowledgeNodeOn(ctx context.Context, q queryer, bankID, nodeID string) (KnowledgeNode, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return KnowledgeNode{}, schemaErr
	}
	query := `SELECT ` + knowledgeSelect + ` FROM ` + knowledgeFrom(schema) + `
	          WHERE kp.bank_id = $1 AND kp.id = $2`
	node, err := scanKnowledgeNode(q.QueryRowContext(ctx, query, bankID, nodeID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return KnowledgeNode{}, ErrKnowledgeNodeNotFound
		}
		return KnowledgeNode{}, err
	}
	return node, nil
}

// PutKnowledgeNode inserts or replaces a knowledge node. A page is backed by a
// mental model, which holds its text and tags; the model is created on the
// first write and updated from then on.
func (s *PostgresStore) PutKnowledgeNode(ctx context.Context, node KnowledgeNode) (KnowledgeNode, error) {
	if node.ID == "" || node.BankID == "" {
		return KnowledgeNode{}, ErrInvalidOperationID
	}
	if node.Kind != KnowledgeKindFolder && node.Kind != KnowledgeKindPage {
		return KnowledgeNode{}, ErrInvalidOperationID
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeNode{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.putKnowledgeNodeTx(ctx, tx, node); err != nil {
		return KnowledgeNode{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeNode{}, err
	}
	return s.GetKnowledgeNode(ctx, node.BankID, node.ID)
}

// insertKnowledgePageModel creates the mental model that backs a page. The
// subtype is pinned, which is what the reference creates for an authored page,
// and the source query is the page name so the model is readable on its own.
func (s *PostgresStore) insertKnowledgePageModel(ctx context.Context, tx Tx, node KnowledgeNode, modelID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	// The page name is bound separately to the model's name (VARCHAR) and its
	// source query (TEXT): one parameter cannot be inferred as two types.
	query := `INSERT INTO ` + quoteIdent(schema) + `.mental_models
	          (id, bank_id, subtype, name, description, source_query, content, tags)
	          VALUES ($1, $2, $3, $4, ' ', $5, $6, $7)`
	_, err := tx.ExecContext(ctx, query, modelID, node.BankID, string(SubtypePinned),
		node.Title, node.Title, node.Text, normalizeTags(node.Tags))
	return err
}

// updateKnowledgePageModel carries a page's text and tags onto its model, so
// the page and the content it shows cannot disagree.
//
// The model's searchable document is its name and content, so a rename — or any
// write that moves either half — has to rebuild the vector too. Without it the
// stored embedding keeps describing the old title and the hybrid search's
// vector arm matches on text the page no longer has. The rebuild is skipped when
// neither half moved, so creating a page (already embedded at insert) and a
// no-op write do not pay for a second embedding call.
func (s *PostgresStore) updateKnowledgePageModel(ctx context.Context, tx Tx, node KnowledgeNode, modelID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	qualified := quoteIdent(schema)
	var oldName, oldContent string
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(name, ''), COALESCE(content, '') FROM `+qualified+`.mental_models
		 WHERE bank_id = $1 AND id = $2`, node.BankID, modelID).Scan(&oldName, &oldContent); err != nil {
		return err
	}
	query := `UPDATE ` + qualified + `.mental_models
	          SET name = $3, content = $4, tags = $5, last_updated = now()`
	args := []any{node.BankID, modelID, node.Title, node.Text, normalizeTags(node.Tags)}
	if node.Title != oldName || node.Text != oldContent {
		embedding, err := mentalModelEmbeddingArg(ctx, s.observationEmbedder, node.Title, node.Text)
		if err != nil {
			return err
		}
		query += `, embedding = $6::vector`
		args = append(args, embedding)
	}
	query += ` WHERE bank_id = $1 AND id = $2`
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// DeleteKnowledgeNode removes a node and its descendants.
//
// The walk comes first because a page's backing mental model would otherwise
// outlive the page that named it. The page rows themselves go through the
// parent cascade.
func (s *PostgresStore) DeleteKnowledgeNode(ctx context.Context, bankID, nodeID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	qualified := quoteIdent(schema)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// The walk is bounded by a path of already-seen ids: a parent cycle (a tree
	// corrupted before the bank lock existed, or restored from such an export) is
	// not forbidden by the foreign key, and an unguarded recursion would spin
	// until the statement budget was exhausted.
	subtree := `WITH RECURSIVE doomed AS (
	                 SELECT id, mental_model_id, ARRAY[id::text] AS path
	                 FROM ` + qualified + `.knowledge_pages
	                 WHERE bank_id = $1 AND id = $2
	                 UNION ALL
	                 SELECT child.id, child.mental_model_id, parent.path || child.id::text
	                 FROM ` + qualified + `.knowledge_pages child
	                 JOIN doomed parent ON child.parent_id = parent.id
	                 WHERE child.bank_id = $1 AND NOT child.id = ANY(parent.path)
	             )`
	rows, err := tx.QueryContext(ctx,
		subtree+` SELECT id, mental_model_id FROM doomed`, bankID, nodeID)
	if err != nil {
		return err
	}
	pageIDs := make([]string, 0)
	modelIDs := make([]string, 0)
	for rows.Next() {
		var pageID string
		var modelID *string
		if err := rows.Scan(&pageID, &modelID); err != nil {
			_ = rows.Close()
			return err
		}
		pageIDs = append(pageIDs, pageID)
		if modelID != nil && *modelID != "" {
			modelIDs = append(modelIDs, *modelID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if len(pageIDs) == 0 {
		return ErrKnowledgeNodeNotFound
	}

	// The subtree's rows are removed explicitly, and before their models: a
	// page's mental_model_id carries ON DELETE CASCADE, so deleting the model
	// first would take the page row with it and leave this statement affecting
	// nothing — the delete would look like a miss on a node that existed.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.knowledge_pages WHERE bank_id = $1 AND id = ANY($2::text[])`,
		bankID, pageIDs); err != nil {
		return err
	}

	// The models are removed after their pages, so a page's model does not
	// outlive the page that named it.
	if len(modelIDs) > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+qualified+`.mental_models WHERE bank_id = $1 AND id = ANY($2::text[])`,
			bankID, modelIDs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// knowledgeRRFK is the reciprocal-rank-fusion smoothing constant. It is the
// same 60 the recall path fuses its arms with, so the two surfaces rank alike.
const knowledgeRRFK = int(rrfConstant)

// knowledgeRRFNorm maps one arm's raw RRF term onto 0..1: 1/(k+rank) tops out at
// 1/(k+1), so multiplying by k+1 puts a single-arm top hit at 1.0 and a hit both
// arms found — each contributing half — at 1.0 too. A page one arm ranked first
// and the other missed therefore reports 0.5. The score is a *rank* score: it
// says where a page placed, not how well it matched, so it is only comparable
// within one result set.
const knowledgeRRFNorm = knowledgeRRFK + 1

// knowledgeSearchLimit bounds a knowledge-search result count the same way on
// both backends, so the two cannot disagree on how many hits a query yields: a
// non-positive limit falls back to the route's default page size and an
// over-large one is capped.
func knowledgeSearchLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 200 {
		return 200
	}
	return limit
}

// knowledgeBM25Query builds the text the tsquery arms bind: the query's terms
// joined disjunctively ("tok | tok"), the reference's native-backend form. An
// OR query means a multi-word question matches a page that carries only one of
// its words, which a conjunctive parse would miss; a term with no word
// characters is dropped, and an empty result means there is nothing to match
// on. Every term is letters and digits only, so it can never carry tsquery
// syntax.
func knowledgeBM25Query(query string) string {
	tokens := knowledgeQueryTerms(query)
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " | ")
}

// SearchKnowledgeNodes searches a bank's pages by a hybrid of full-text match
// (ts_rank_cd) over the page name and content and vector similarity over the
// backing mental model's embedding, fused with reciprocal-rank fusion. Folders
// are excluded. Pages with no embedding are skipped by the vector arm but still
// rank on the text arm, and a caller with no query embedding gets the text arm
// alone. The reported score is normalised to 0..1 (see knowledgeRRFNorm).
func (s *PostgresStore) SearchKnowledgeNodes(ctx context.Context, query KnowledgeSearchQuery) ([]KnowledgeHit, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []KnowledgeHit
		return zero0, schemaErr
	}
	limit := knowledgeSearchLimit(query.Limit)
	term := strings.TrimSpace(query.Query)
	if term == "" {
		return []KnowledgeHit{}, nil
	}
	bm25Text := knowledgeBM25Query(term)
	if query.DisableTextArm {
		bm25Text = ""
	}
	embedding := query.Embedding
	if len(embedding) > 0 {
		if err := ValidateEmbedding(embedding); err != nil {
			return nil, err
		}
	}
	// With neither a term to match on nor an embedding to rank by there is
	// nothing to search with, so the answer is empty rather than every page.
	if bm25Text == "" && len(embedding) == 0 {
		return []KnowledgeHit{}, nil
	}
	// Over-fetch each arm so fusion has room to reorder before the final cut.
	fetch := limit * 4
	if fetch < 40 {
		fetch = 40
	}
	if fetch > 200 {
		fetch = 200
	}
	norm := strconv.Itoa(knowledgeRRFNorm)
	rrfK := strconv.Itoa(knowledgeRRFK)
	pageID := `COALESCE(vec.page_id, bm.page_id)`

	var (
		sqlQuery string
		args     []any
	)
	switch {
	case len(embedding) > 0 && bm25Text != "":
		// Two arms, each ranked independently, then fused (k=60) in SQL. The
		// vector arm ranks by cosine distance; the text arm by ts_rank_cd over
		// the generated search_vector, which is built from name and content.
		// The tag predicate is bound into both arms' WHERE, so the filter is
		// applied before ranking (the five binds of the fused query precede it).
		tagPred, tagArgs := knowledgeTagClause(query.Tags, query.TagsMatch, query.TagGroups, 5)
		sqlQuery = `WITH vec AS (
		                SELECT kp.id AS page_id,
		                       ROW_NUMBER() OVER (ORDER BY mm.embedding <=> $1::vector, kp.id) AS rnk
		                FROM ` + knowledgeFrom(schema) + `
		                WHERE kp.bank_id = $2 AND kp.kind = 'page' AND mm.embedding IS NOT NULL ` + tagPred + `
		                ORDER BY mm.embedding <=> $1::vector, kp.id
		                LIMIT $3
		            ),
		            bm AS (
		                SELECT kp.id AS page_id,
		                       ROW_NUMBER() OVER (ORDER BY ts_rank_cd(mm.search_vector, to_tsquery('english', $4)) DESC, kp.id) AS rnk
		                FROM ` + knowledgeFrom(schema) + `
		                WHERE kp.bank_id = $2 AND kp.kind = 'page'
		                      AND mm.search_vector @@ to_tsquery('english', $4) ` + tagPred + `
		                ORDER BY ts_rank_cd(mm.search_vector, to_tsquery('english', $4)) DESC, kp.id
		                LIMIT $3
		            ),
		            fused AS (
		                SELECT ` + pageID + ` AS page_id,
		                       (` + norm + `.0 / 2)
		                       * (COALESCE(1.0 / (` + rrfK + ` + vec.rnk), 0)
		                          + COALESCE(1.0 / (` + rrfK + ` + bm.rnk), 0)) AS score
		                FROM vec FULL OUTER JOIN bm ON vec.page_id = bm.page_id
		            )
		            SELECT ` + knowledgeSelect + `, f.score
		            FROM fused f
		            JOIN ` + quoteIdent(schema) + `.knowledge_pages kp ON kp.id = f.page_id AND kp.bank_id = $2
		            LEFT JOIN ` + quoteIdent(schema) + `.mental_models mm
		                   ON mm.id = kp.mental_model_id AND mm.bank_id = kp.bank_id
		            ORDER BY f.score DESC, kp.id ASC
		            LIMIT $5`
		args = append([]any{EncodeEmbedding(embedding), query.BankID, fetch, bm25Text, limit}, tagArgs...)
	case len(embedding) > 0:
		// Vector-only: the query carried nothing to match on, so rank straight
		// off the ANN scan. One arm at rank 1 reports the 1.0 ceiling.
		tagPred, tagArgs := knowledgeTagClause(query.Tags, query.TagsMatch, query.TagGroups, 3)
		sqlQuery = `SELECT ` + knowledgeSelect + `,
		                   ` + norm + `.0 / (` + rrfK + ` + ROW_NUMBER() OVER (
		                         ORDER BY mm.embedding <=> $1::vector, kp.id)) AS score
		            FROM ` + knowledgeFrom(schema) + `
		            WHERE kp.bank_id = $2 AND kp.kind = 'page' AND mm.embedding IS NOT NULL ` + tagPred + `
		            ORDER BY mm.embedding <=> $1::vector, kp.id ASC
		            LIMIT $3`
		args = append([]any{EncodeEmbedding(embedding), query.BankID, limit}, tagArgs...)
	default:
		// Text-only: no arm to fuse with, so rank straight off ts_rank_cd and
		// apply the same normalised RRF curve so the score means the same thing
		// it does on the fused path — a top text hit reports 1.0.
		tagPred, tagArgs := knowledgeTagClause(query.Tags, query.TagsMatch, query.TagGroups, 3)
		sqlQuery = `SELECT ` + knowledgeSelect + `,
		                   ` + norm + `.0
		                   / (` + rrfK + ` + ROW_NUMBER() OVER (
		                         ORDER BY ts_rank_cd(mm.search_vector, to_tsquery('english', $2)) DESC, kp.id)) AS score
		            FROM ` + knowledgeFrom(schema) + `
		            WHERE kp.bank_id = $1 AND kp.kind = 'page'
		                  AND mm.search_vector @@ to_tsquery('english', $2) ` + tagPred + `
		            ORDER BY ts_rank_cd(mm.search_vector, to_tsquery('english', $2)) DESC, kp.id ASC
		            LIMIT $3`
		args = append([]any{query.BankID, bm25Text, limit}, tagArgs...)
	}
	rows, err := s.reader().QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	hits := make([]KnowledgeHit, 0)
	for rows.Next() {
		var node KnowledgeNode
		var score float64
		if err := scanKnowledgeWithScore(rows, &node, &score); err != nil {
			return nil, err
		}
		hits = append(hits, KnowledgeHit{Node: node, Score: score})
	}
	return hits, rows.Err()
}

// knowledgeTagClause renders the tag predicate a knowledge search binds into
// each arm's WHERE, so the filter is applied before ranking (the reference
// builds the same clause from its KnowledgeTagFilter). The column is left
// unqualified: knowledge_pages carries no tags column, so `tags` can only be
// the mental model's, and the bare form is what the dialect rewriters expect.
// `used` is the number of parameters already bound; the predicate's
// placeholders continue from there.
func knowledgeTagClause(tags []string, mode string, groups []TagGroup, used int) (string, []any) {
	flat, flatArgs := knowledgeFlatTagClause(tags, mode, used)
	groupClause, groupArgs := renderTagGroups(groups, used+len(flatArgs))
	switch {
	case flat == "" && groupClause == "":
		return "", nil
	case flat == "":
		return " AND " + groupClause, groupArgs
	case groupClause == "":
		return " AND " + flat, flatArgs
	default:
		return " AND " + flat + " AND " + groupClause, append(flatArgs, groupArgs...)
	}
}

// knowledgeFlatTagClause is listTagClause over the knowledge join's `tags`
// column (the mental model's), with the same mode semantics: an empty tag set
// filters nothing except under `exact`, which selects the untagged scope;
// `any`/`all` admit untagged rows and the `_strict` modes exclude them.
func knowledgeFlatTagClause(tags []string, mode string, used int) (string, []any) {
	if len(tags) == 0 {
		if mode == TagsMatchExact {
			return "(tags IS NULL OR tags = '{}')", nil
		}
		return "", nil
	}
	placeholder := "$" + itoa(used+1)
	switch mode {
	case TagsMatchExact:
		return "(tags @> " + placeholder + " AND tags <@ " + placeholder + ")", []any{tags}
	case TagsMatchAll:
		return "(tags IS NULL OR tags = '{}' OR tags @> " + placeholder + ")", []any{tags}
	case TagsMatchAllStrict:
		return "(tags IS NOT NULL AND tags != '{}' AND tags @> " + placeholder + ")", []any{tags}
	case TagsMatchAnyStrict:
		return "(tags IS NOT NULL AND tags != '{}' AND tags && " + placeholder + ")", []any{tags}
	default: // any
		return "(tags IS NULL OR tags = '{}' OR tags && " + placeholder + ")", []any{tags}
	}
}

// scanKnowledgeNode reads the shared projection.
func scanKnowledgeNode(row rowScanner) (KnowledgeNode, error) {
	var node KnowledgeNode
	var kind, tags string
	if err := row.Scan(&node.ID, &node.BankID, &node.ParentID, &kind, &node.Title,
		&node.MentalModelID, &node.Text, &tags, &node.Managed, &node.SortOrder, &node.CreatedAt, &node.UpdatedAt); err != nil {
		return KnowledgeNode{}, err
	}
	node.Kind = KnowledgeKind(kind)
	node.Tags = decodeTextArray(tags)
	return node, nil
}

// scanKnowledgeWithScore reads the projection plus a ranking score.
func scanKnowledgeWithScore(row rowScanner, node *KnowledgeNode, score *float64) error {
	var kind, tags string
	if err := row.Scan(&node.ID, &node.BankID, &node.ParentID, &kind, &node.Title,
		&node.MentalModelID, &node.Text, &tags, &node.Managed, &node.SortOrder, &node.CreatedAt, &node.UpdatedAt, score); err != nil {
		return err
	}
	node.Kind = KnowledgeKind(kind)
	node.Tags = decodeTextArray(tags)
	return nil
}
