package storage

import "context"

// The knowledge-base patch reaches two rows — the node and, for a page, the
// mental model that carries its text, tags and options — and the reference
// applies the whole patch in one transaction. These are the write paths that do
// the same here: a caller that changes both rows gets both or neither, and no
// reader observes the node renamed while its model still carries the old name.

// UpdateKnowledgeNode applies a partial update.
func (s *PostgresStore) UpdateKnowledgeNode(ctx context.Context, bankID, nodeID string, patch KnowledgeNodePatch) (KnowledgeNode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeNode{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.updateKnowledgeNodeTx(ctx, tx, bankID, nodeID, patch); err != nil {
		return KnowledgeNode{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeNode{}, err
	}
	return s.GetKnowledgeNode(ctx, bankID, nodeID)
}

// UpdateKnowledgeNodeWithModel applies a node patch and, when one is given, a
// patch to the model backing the node in one transaction, so the two rows change
// as a unit: a rename must not survive the option update that fails after it (a
// model name the column rejects, a vector the deployment's dimension rejects),
// and no reader sees the node renamed while its model still carries the old
// name.
func (s *PostgresStore) UpdateKnowledgeNodeWithModel(ctx context.Context, bankID, nodeID string, patch KnowledgeNodePatch, modelPatch *MentalModelPatch) (KnowledgeNode, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeNode{}, err
	}
	defer func() { _ = tx.Rollback() }()
	node, err := s.updateKnowledgeNodeTx(ctx, tx, bankID, nodeID, patch)
	if err != nil {
		return KnowledgeNode{}, err
	}
	if modelPatch != nil {
		if node.Kind != KnowledgeKindPage || node.MentalModelID == "" {
			return KnowledgeNode{}, ErrMentalModelNotFound
		}
		// The model is read inside the transaction, so the patch is applied over
		// the row the write will replace rather than over a copy read before it.
		model, err := s.getMentalModelOn(ctx, tx, bankID, node.MentalModelID)
		if err != nil {
			return KnowledgeNode{}, err
		}
		applyMentalModelPatch(&model, *modelPatch)
		if err := s.putMentalModelOn(ctx, tx, model); err != nil {
			return KnowledgeNode{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeNode{}, err
	}
	return s.GetKnowledgeNode(ctx, bankID, nodeID)
}

// updateKnowledgeNodeTx applies a partial node patch through the given
// transaction: the node is read, patched and written back on that connection, so
// a caller that pairs the node write with another row gets both or neither.
func (s *PostgresStore) updateKnowledgeNodeTx(ctx context.Context, tx Tx, bankID, nodeID string, patch KnowledgeNodePatch) (KnowledgeNode, error) {
	node, err := s.getKnowledgeNodeOn(ctx, tx, bankID, nodeID)
	if err != nil {
		return KnowledgeNode{}, err
	}
	applyKnowledgeNodePatch(&node, patch)
	if err := s.putKnowledgeNodeTx(ctx, tx, node); err != nil {
		return KnowledgeNode{}, err
	}
	return node, nil
}

// putKnowledgeNodeTx writes one node and, for a page, the model that carries its
// text and tags, through the given transaction.
func (s *PostgresStore) putKnowledgeNodeTx(ctx context.Context, tx Tx, node KnowledgeNode) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	modelID := ""
	if node.Kind == KnowledgeKindPage {
		modelID = node.MentalModelID
		if modelID == "" {
			modelID = newUUID()
			if err := s.insertKnowledgePageModel(ctx, tx, node, modelID); err != nil {
				return err
			}
		} else if err := s.updateKnowledgePageModel(ctx, tx, node, modelID); err != nil {
			return err
		}
	}

	query := `INSERT INTO ` + quoteIdent(schema) + `.knowledge_pages
	          (id, bank_id, parent_id, kind, name, mental_model_id, managed, sort_order, created_at, updated_at)
	          VALUES ($1,$2,NULLIF($3,''),$4,$5,NULLIF($6,''),$7,$8, now(), now())
	          ON CONFLICT (id) DO UPDATE SET
	              parent_id = EXCLUDED.parent_id, kind = EXCLUDED.kind, name = EXCLUDED.name,
	              mental_model_id = EXCLUDED.mental_model_id, managed = EXCLUDED.managed,
	              sort_order = EXCLUDED.sort_order, updated_at = now()`
	_, err := tx.ExecContext(ctx, query, node.ID, node.BankID, node.ParentID,
		string(node.Kind), node.Title, modelID, node.Managed, node.SortOrder)
	return err
}
