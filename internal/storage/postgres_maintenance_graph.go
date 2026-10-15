package storage

import "context"

// The Postgres store backs the graph-maintenance job's queues.
var _ GraphMaintainer = (*PostgresStore)(nil)

// DrainGraphMaintenance claims up to limit queued graph-relink targets and
// removes them from the queue, so a deleted unit's neighbours are relinked
// exactly once.
func (s *PostgresStore) DrainGraphMaintenance(ctx context.Context, limit int) ([]MaintenanceTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	qualified := quoteIdent(s.schema)
	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM `+qualified+`.graph_maintenance_queue
		 WHERE (bank_id, unit_id) IN (
		     SELECT bank_id, unit_id FROM `+qualified+`.graph_maintenance_queue
		     ORDER BY enqueued_at ASC LIMIT $1)
		 RETURNING bank_id, unit_id::text`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	targets := make([]MaintenanceTarget, 0)
	for rows.Next() {
		var target MaintenanceTarget
		if err := rows.Scan(&target.BankID, &target.ID); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// RequeueGraphMaintenance puts relink targets back on the queue after a failed
// drain, so the work is retried rather than lost with the claimed row. A target
// already queued is left alone.
func (s *PostgresStore) RequeueGraphMaintenance(ctx context.Context, targets []MaintenanceTarget) error {
	if len(targets) == 0 {
		return nil
	}
	qualified := quoteIdent(s.schema)
	banks := make([]string, len(targets))
	units := make([]string, len(targets))
	for i, target := range targets {
		banks[i] = target.BankID
		units[i] = target.ID
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.graph_maintenance_queue (bank_id, unit_id)
		 SELECT bank_id, unit_id FROM unnest($1::text[], $2::uuid[]) AS t(bank_id, unit_id)
		 ON CONFLICT (bank_id, unit_id) DO NOTHING`,
		banks, units)
	return err
}

// RequeueEntityMaintenance puts entity-prune targets back on the queue after a
// failed drain, so the work is retried rather than lost with the claimed row.
// A target already queued is left alone.
func (s *PostgresStore) RequeueEntityMaintenance(ctx context.Context, targets []MaintenanceTarget) error {
	if len(targets) == 0 {
		return nil
	}
	qualified := quoteIdent(s.schema)
	banks := make([]string, len(targets))
	entities := make([]string, len(targets))
	for i, target := range targets {
		banks[i] = target.BankID
		entities[i] = target.ID
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.entity_maintenance_queue (bank_id, entity_id)
		 SELECT bank_id, entity_id FROM unnest($1::text[], $2::uuid[]) AS t(bank_id, entity_id)
		 ON CONFLICT (bank_id, entity_id) DO NOTHING`,
		banks, entities)
	return err
}

// RelinkUnit recomputes one unit's semantic links from its embedding: the
// existing semantic edges touching it are dropped and rebuilt from the vector
// neighbours above the similarity floor.
func (s *PostgresStore) RelinkUnit(ctx context.Context, bankID, unitID string) error {
	qualified := quoteIdent(s.schema)
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_links
		 WHERE bank_id = $1 AND link_type = 'semantic' AND (from_unit_id = $2::uuid OR to_unit_id = $2::uuid)`,
		bankID, unitID); err != nil {
		return err
	}
	return s.createSemanticLinks(ctx, bankID, []string{unitID})
}

// DrainEntityMaintenance claims up to limit queued entity-prune targets and
// removes them from the queue.
func (s *PostgresStore) DrainEntityMaintenance(ctx context.Context, limit int) ([]MaintenanceTarget, error) {
	if limit <= 0 {
		limit = 100
	}
	qualified := quoteIdent(s.schema)
	rows, err := s.db.QueryContext(ctx,
		`DELETE FROM `+qualified+`.entity_maintenance_queue
		 WHERE (bank_id, entity_id) IN (
		     SELECT bank_id, entity_id FROM `+qualified+`.entity_maintenance_queue
		     ORDER BY enqueued_at ASC LIMIT $1)
		 RETURNING bank_id, entity_id::text`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	targets := make([]MaintenanceTarget, 0)
	for rows.Next() {
		var target MaintenanceTarget
		if err := rows.Scan(&target.BankID, &target.ID); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// PruneEntity removes an entity no memory unit mentions any more. An entity
// that regained a mention since it was queued is kept.
func (s *PostgresStore) PruneEntity(ctx context.Context, bankID, entityID string) error {
	qualified := quoteIdent(s.schema)
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.entities
		 WHERE bank_id = $1 AND id = $2::uuid
		   AND NOT EXISTS (SELECT 1 FROM `+qualified+`.unit_entities ue WHERE ue.entity_id = $2::uuid)`,
		bankID, entityID)
	return err
}
