package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// knowledgeSelect is the shared projection for the knowledge tree. A row
// carries the name and the position; a page's text, tags and description live
// on the mental model the row points at, so the projection joins both and a
// caller reads one node rather than two rows.
const knowledgeSelect = `kp.id, kp.bank_id, COALESCE(kp.parent_id, ''), kp.kind, kp.name,
       COALESCE(kp.mental_model_id, ''), COALESCE(mm.content, ''), COALESCE(mm.tags, '{}')::text,
       kp.created_at, kp.updated_at`

// knowledgeFrom is the shared source. The join is on the pair, not the model id
// alone, because a mental model identifier is unique within its bank.
func (s *PostgresStore) knowledgeFrom() string {
	return quoteIdent(s.schema) + `.knowledge_pages kp
	        LEFT JOIN ` + quoteIdent(s.schema) + `.mental_models mm
	               ON mm.id = kp.mental_model_id AND mm.bank_id = kp.bank_id`
}

// ListKnowledgeNodes returns every knowledge node of one bank, ordered so the
// tree can be nested without re-sorting: siblings in display order.
func (s *PostgresStore) ListKnowledgeNodes(ctx context.Context, bankID string) ([]KnowledgeNode, error) {
	query := `SELECT ` + knowledgeSelect + ` FROM ` + s.knowledgeFrom() + `
	          WHERE kp.bank_id = $1
	          ORDER BY kp.parent_id NULLS FIRST, kp.sort_order, kp.name, kp.id`
	rows, err := s.db.QueryContext(ctx, query, bankID)
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
	return out, rows.Err()
}

// GetKnowledgeNode returns one knowledge node by identifier.
func (s *PostgresStore) GetKnowledgeNode(ctx context.Context, bankID, nodeID string) (KnowledgeNode, error) {
	query := `SELECT ` + knowledgeSelect + ` FROM ` + s.knowledgeFrom() + `
	          WHERE kp.bank_id = $1 AND kp.id = $2`
	node, err := scanKnowledgeNode(s.db.QueryRowContext(ctx, query, bankID, nodeID))
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

	modelID := ""
	if node.Kind == KnowledgeKindPage {
		modelID = node.MentalModelID
		if modelID == "" {
			modelID = newUUID()
			if err := s.insertKnowledgePageModel(ctx, tx, node, modelID); err != nil {
				return KnowledgeNode{}, err
			}
		} else if err := s.updateKnowledgePageModel(ctx, tx, node, modelID); err != nil {
			return KnowledgeNode{}, err
		}
	}

	query := `INSERT INTO ` + quoteIdent(s.schema) + `.knowledge_pages
	          (id, bank_id, parent_id, kind, name, mental_model_id, created_at, updated_at)
	          VALUES ($1,$2,NULLIF($3,''),$4,$5,NULLIF($6,''), now(), now())
	          ON CONFLICT (id) DO UPDATE SET
	              parent_id = EXCLUDED.parent_id, kind = EXCLUDED.kind, name = EXCLUDED.name,
	              mental_model_id = EXCLUDED.mental_model_id, updated_at = now()`
	if _, err := tx.ExecContext(ctx, query, node.ID, node.BankID, node.ParentID,
		string(node.Kind), node.Title, modelID); err != nil {
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
	// The page name is bound separately to the model's name (VARCHAR) and its
	// source query (TEXT): one parameter cannot be inferred as two types.
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.mental_models
	          (id, bank_id, subtype, name, description, source_query, content, tags)
	          VALUES ($1, $2, $3, $4, ' ', $5, $6, $7)`
	_, err := tx.ExecContext(ctx, query, modelID, node.BankID, string(SubtypePinned),
		node.Title, node.Title, node.Text, normalizeTags(node.Tags))
	return err
}

// updateKnowledgePageModel carries a page's text and tags onto its model, so
// the page and the content it shows cannot disagree.
func (s *PostgresStore) updateKnowledgePageModel(ctx context.Context, tx Tx, node KnowledgeNode, modelID string) error {
	query := `UPDATE ` + quoteIdent(s.schema) + `.mental_models
	          SET name = $3, content = $4, tags = $5, last_updated = now()
	          WHERE bank_id = $1 AND id = $2`
	_, err := tx.ExecContext(ctx, query, node.BankID, modelID,
		node.Title, node.Text, normalizeTags(node.Tags))
	return err
}

// UpdateKnowledgeNode applies a partial update.
func (s *PostgresStore) UpdateKnowledgeNode(ctx context.Context, bankID, nodeID string, patch KnowledgeNodePatch) (KnowledgeNode, error) {
	current, err := s.GetKnowledgeNode(ctx, bankID, nodeID)
	if err != nil {
		return KnowledgeNode{}, err
	}
	if patch.Title != nil {
		current.Title = *patch.Title
	}
	if patch.Text != nil {
		current.Text = *patch.Text
	}
	if patch.ParentID != nil {
		current.ParentID = *patch.ParentID
	}
	if patch.Tags != nil {
		current.Tags = append([]string(nil), (*patch.Tags)...)
	}
	return s.PutKnowledgeNode(ctx, current)
}

// DeleteKnowledgeNode removes a node and its descendants.
//
// The walk comes first because a page's backing mental model would otherwise
// outlive the page that named it. The page rows themselves go through the
// parent cascade.
func (s *PostgresStore) DeleteKnowledgeNode(ctx context.Context, bankID, nodeID string) error {
	qualified := quoteIdent(s.schema)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	subtree := `WITH RECURSIVE doomed AS (
	                 SELECT id, mental_model_id FROM ` + qualified + `.knowledge_pages
	                 WHERE bank_id = $1 AND id = $2
	                 UNION ALL
	                 SELECT child.id, child.mental_model_id
	                 FROM ` + qualified + `.knowledge_pages child
	                 JOIN doomed parent ON child.parent_id = parent.id
	                 WHERE child.bank_id = $1
	             )`
	rows, err := tx.QueryContext(ctx,
		subtree+` SELECT mental_model_id FROM doomed WHERE mental_model_id IS NOT NULL`, bankID, nodeID)
	if err != nil {
		return err
	}
	modelIDs := make([]string, 0)
	for rows.Next() {
		var modelID string
		if err := rows.Scan(&modelID); err != nil {
			_ = rows.Close()
			return err
		}
		modelIDs = append(modelIDs, modelID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	if len(modelIDs) > 0 {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+qualified+`.mental_models WHERE bank_id = $1 AND id = ANY($2::text[])`,
			bankID, modelIDs); err != nil {
			return err
		}
	}

	result, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.knowledge_pages WHERE bank_id = $1 AND id = $2`, bankID, nodeID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrKnowledgeNodeNotFound
	}
	return tx.Commit()
}

// SearchKnowledgeNodes searches a bank's pages by name and body. A name hit
// outranks a body hit, so the ordering matches the ranking a caller sees rather
// than an arbitrary row order.
func (s *PostgresStore) SearchKnowledgeNodes(ctx context.Context, bankID, query string, limit int) ([]KnowledgeHit, error) {
	limit = clampLimit(limit, 50)
	term := strings.TrimSpace(query)
	sqlQuery := `SELECT ` + knowledgeSelect + `,
	                    CASE WHEN kp.name ILIKE '%' || $2 || '%' THEN 1.0
	                         WHEN COALESCE(mm.content,'') ILIKE '%' || $2 || '%' THEN 0.5
	                         ELSE 0 END AS score
	             FROM ` + s.knowledgeFrom() + `
	             WHERE kp.bank_id = $1 AND kp.kind = 'page'
	               AND ($2 = '' OR kp.name ILIKE '%' || $2 || '%'
	                            OR COALESCE(mm.content,'') ILIKE '%' || $2 || '%')
	             ORDER BY score DESC, kp.id ASC LIMIT $3`
	rows, err := s.db.QueryContext(ctx, sqlQuery, bankID, term, limit)
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

// scanKnowledgeNode reads the shared projection.
func scanKnowledgeNode(row rowScanner) (KnowledgeNode, error) {
	var node KnowledgeNode
	var kind, tags string
	if err := row.Scan(&node.ID, &node.BankID, &node.ParentID, &kind, &node.Title,
		&node.MentalModelID, &node.Text, &tags, &node.CreatedAt, &node.UpdatedAt); err != nil {
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
		&node.MentalModelID, &node.Text, &tags, &node.CreatedAt, &node.UpdatedAt, score); err != nil {
		return err
	}
	node.Kind = KnowledgeKind(kind)
	node.Tags = decodeTextArray(tags)
	return nil
}
