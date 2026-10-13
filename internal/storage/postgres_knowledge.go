package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// knowledgeSelect is the shared projection for the knowledge tree.
// knowledgeSelect is the shared projection for knowledge nodes. The tag array
// is rendered to text so it can be scanned without a driver-specific array
// codec, and decoded by the scanners below.
const knowledgeSelect = `id, bank_id, COALESCE(parent_id,''), kind, title, COALESCE(text,''),
       COALESCE(tags, '{}')::text, created_at, updated_at`

// ListKnowledgeNodes returns every knowledge node of one bank, oldest first.
func (s *PostgresStore) ListKnowledgeNodes(ctx context.Context, bankID string) ([]KnowledgeNode, error) {
	query := `SELECT ` + knowledgeSelect + ` FROM ` + quoteIdent(s.schema) + `.knowledge_nodes
	          WHERE bank_id = $1 ORDER BY created_at ASC, id ASC`
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
	query := `SELECT ` + knowledgeSelect + ` FROM ` + quoteIdent(s.schema) + `.knowledge_nodes
	          WHERE bank_id = $1 AND id = $2`
	node, err := scanKnowledgeNode(s.db.QueryRowContext(ctx, query, bankID, nodeID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return KnowledgeNode{}, ErrKnowledgeNodeNotFound
		}
		return KnowledgeNode{}, err
	}
	return node, nil
}

// PutKnowledgeNode inserts or replaces a knowledge node.
func (s *PostgresStore) PutKnowledgeNode(ctx context.Context, node KnowledgeNode) (KnowledgeNode, error) {
	if node.ID == "" || node.BankID == "" {
		return KnowledgeNode{}, ErrInvalidOperationID
	}
	if node.Kind != KnowledgeKindFolder && node.Kind != KnowledgeKindPage {
		return KnowledgeNode{}, ErrInvalidOperationID
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.knowledge_nodes
	          (id, bank_id, parent_id, kind, title, text, tags, created_at, updated_at)
	          VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7, now(), now())
	          ON CONFLICT (id, bank_id) DO UPDATE SET
	              parent_id = EXCLUDED.parent_id, kind = EXCLUDED.kind, title = EXCLUDED.title,
	              text = EXCLUDED.text, tags = EXCLUDED.tags, updated_at = now()`
	if _, err := s.db.ExecContext(ctx, query, node.ID, node.BankID, node.ParentID,
		string(node.Kind), node.Title, node.Text, normalizeTags(node.Tags)); err != nil {
		return KnowledgeNode{}, err
	}
	return s.GetKnowledgeNode(ctx, node.BankID, node.ID)
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
// Descendants are found with a recursive walk before the delete, so a folder
// cannot leave unreachable pages behind.
func (s *PostgresStore) DeleteKnowledgeNode(ctx context.Context, bankID, nodeID string) error {
	qualified := quoteIdent(s.schema)
	query := `WITH RECURSIVE doomed AS (
	              SELECT id FROM ` + qualified + `.knowledge_nodes WHERE bank_id = $1 AND id = $2
	              UNION ALL
	              SELECT child.id FROM ` + qualified + `.knowledge_nodes child
	              JOIN doomed parent ON child.parent_id = parent.id
	              WHERE child.bank_id = $1
	          )
	          DELETE FROM ` + qualified + `.knowledge_nodes
	          WHERE bank_id = $1 AND id IN (SELECT id FROM doomed)`
	result, err := s.db.ExecContext(ctx, query, bankID, nodeID)
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
	return nil
}

// SearchKnowledgeNodes searches a bank's pages by title and text.
func (s *PostgresStore) SearchKnowledgeNodes(ctx context.Context, bankID, query string, limit int) ([]KnowledgeHit, error) {
	limit = clampLimit(limit, 50)
	term := strings.TrimSpace(query)
	// A title hit outranks a body hit, so the ordering matches the ranking a
	// caller sees rather than an arbitrary row order.
	sqlQuery := `SELECT ` + knowledgeSelect + `,
	                    CASE WHEN title ILIKE '%' || $2 || '%' THEN 1.0
	                         WHEN COALESCE(text,'') ILIKE '%' || $2 || '%' THEN 0.5
	                         ELSE 0 END AS score
	             FROM ` + quoteIdent(s.schema) + `.knowledge_nodes
	             WHERE bank_id = $1 AND kind = 'page'
	               AND ($2 = '' OR title ILIKE '%' || $2 || '%' OR COALESCE(text,'') ILIKE '%' || $2 || '%')
	             ORDER BY score DESC, id ASC LIMIT $3`
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
	var kind string
	var tags string
	if err := row.Scan(&node.ID, &node.BankID, &node.ParentID, &kind, &node.Title,
		&node.Text, &tags, &node.CreatedAt, &node.UpdatedAt); err != nil {
		return KnowledgeNode{}, err
	}
	node.Kind = KnowledgeKind(kind)
	node.Tags = decodeTextArray(tags)
	return node, nil
}

// scanKnowledgeWithScore reads the projection plus a ranking score.
func scanKnowledgeWithScore(row rowScanner, node *KnowledgeNode, score *float64) error {
	var kind string
	var tags string
	if err := row.Scan(&node.ID, &node.BankID, &node.ParentID, &kind, &node.Title,
		&node.Text, &tags, &node.CreatedAt, &node.UpdatedAt, score); err != nil {
		return err
	}
	node.Kind = KnowledgeKind(kind)
	node.Tags = decodeTextArray(tags)
	return nil
}
