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

// RelinkUnit re-derives the derived links one unit lost, from the same sources
// retain derives them from: its temporal edges are topped up from the units
// whose dates fall inside the window, and its semantic edges from the vector
// neighbours above the similarity floor. It only ever adds an edge, mirroring
// the reference's relink pass: deleting one here would destroy the incoming
// derived edges of a unit that was never told to relink.
func (s *PostgresStore) RelinkUnit(ctx context.Context, bankID, unitID string) error {
	if err := s.topUpTemporalLinks(ctx, bankID, unitID); err != nil {
		return err
	}
	return s.createSemanticLinks(ctx, bankID, []string{unitID})
}

// topUpTemporalLinks adds the temporal edges one unit is missing within the
// window, mirroring the reference's relink top-up: it only runs for dated
// experience/world units still below the per-unit cap, takes the nearest
// same-kind units in both directions inside the 24h window, and leaves any edge
// that is already there alone.
func (s *PostgresStore) topUpTemporalLinks(ctx context.Context, bankID, unitID string) error {
	qualified := quoteIdent(s.schema)
	_, err := s.db.ExecContext(ctx,
		`WITH victim AS (
		     SELECT mu.id, mu.event_date, mu.fact_type
		     FROM `+qualified+`.memory_units mu
		     WHERE mu.bank_id = $1 AND mu.id = $2::uuid
		       AND mu.fact_type IN ('experience', 'world')
		       AND mu.event_date IS NOT NULL
		       AND (SELECT count(*) FROM `+qualified+`.memory_links ml
		            WHERE ml.bank_id = $1 AND ml.from_unit_id = mu.id
		              AND ml.link_type = 'temporal') < $3
		 ), ranked AS (
		     SELECT victim.id AS from_id, near.id AS to_id,
		            ABS(EXTRACT(EPOCH FROM near.event_date - victim.event_date)) / 3600.0 AS diff_hours,
		            ROW_NUMBER() OVER (
		                PARTITION BY victim.id
		                ORDER BY ABS(EXTRACT(EPOCH FROM near.event_date - victim.event_date)), near.id
		            ) AS rn
		     FROM victim
		     CROSS JOIN LATERAL (
		         (SELECT mu.id, mu.event_date FROM `+qualified+`.memory_units mu
		          WHERE mu.bank_id = $1 AND mu.fact_type = victim.fact_type
		            AND mu.event_date <= victim.event_date AND mu.id <> victim.id
		          ORDER BY mu.event_date DESC, mu.id LIMIT $3)
		         UNION ALL
		         (SELECT mu.id, mu.event_date FROM `+qualified+`.memory_units mu
		          WHERE mu.bank_id = $1 AND mu.fact_type = victim.fact_type
		            AND mu.event_date > victim.event_date AND mu.id <> victim.id
		          ORDER BY mu.event_date ASC, mu.id LIMIT $3)
		     ) near
		 )
		 INSERT INTO `+qualified+`.memory_links (bank_id, from_unit_id, to_unit_id, link_type, weight)
		 SELECT $1, from_id, to_id, 'temporal', GREATEST(0.3, 1.0 - diff_hours / $4)
		 FROM ranked WHERE rn <= $3 AND diff_hours <= $4
		 ON CONFLICT DO NOTHING`,
		bankID, unitID, maxTemporalLinksPerUnit, temporalLinkWindowHours)
	return err
}

// enqueueRelinkVictims queues, for a later top-up, the units whose outgoing
// temporal or semantic links point at any of unitIDs — together with unitIDs
// themselves when includeAffected is set, since an edit leaves the edited unit
// live while stripping its own derived adjacency.
//
// It mirrors the reference's enqueue_relink_victims, including its ordering
// rule: it must run before the links are deleted, because once the rows are
// gone the join that finds the survivors returns nothing.
func (s *PostgresStore) enqueueRelinkVictims(ctx context.Context, exec stmtExecer, bankID string, unitIDs []string, includeAffected bool) error {
	if len(unitIDs) == 0 {
		return nil
	}
	qualified := quoteIdent(s.schema)
	_, err := exec.ExecContext(ctx,
		`INSERT INTO `+qualified+`.graph_maintenance_queue (bank_id, unit_id)
		 SELECT $1, victim FROM (
		     SELECT DISTINCT ml.from_unit_id AS victim
		     FROM `+qualified+`.memory_links ml
		     WHERE ml.bank_id = $1 AND ml.to_unit_id = ANY($2::uuid[])
		       AND ml.link_type IN ('temporal', 'semantic')
		       AND NOT (ml.from_unit_id = ANY($2::uuid[]))
		     UNION
		     SELECT id FROM unnest($2::uuid[]) AS affected(id)
		     WHERE $3
		 ) victims
		 ON CONFLICT (bank_id, unit_id) DO NOTHING`,
		bankID, unitIDs, includeAffected)
	return err
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
