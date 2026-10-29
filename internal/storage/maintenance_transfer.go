package storage

import (
	"context"
	"time"
)

// MaintenanceQueueRow is one queued maintenance work marker as a transfer
// archive carries it.
//
// The two queues are keyed by identifiers the import regenerates, so a row is
// carried by whatever the target can resolve: the graph queue by the source
// unit id (which the importing replay maps onto the target's own unit), the
// entity queue by the entity's canonical name (which the target re-resolves
// against its own entities). The reference carries them the same way and drops
// a row whose target did not come back — the target re-enqueues its own
// maintenance as the import writes land, so a dropped marker loses no work.
type MaintenanceQueueRow struct {
	// ID is the target's unit id (graph queue) or entity id (entity queue, on
	// import only). Empty on an exported entity row, which carries a name.
	ID string
	// CanonicalName is an exported entity row's name.
	CanonicalName string
	// EnqueuedAt is when the source instance queued the work.
	EnqueuedAt time.Time
}

// MaintenanceQueueTransfer is the optional capability of a backend that keeps
// the maintenance work queues a whole-bank transfer carries. A backend that does
// not implement it exports without those segments and imports none, which is
// what a store with no such queue has to offer.
type MaintenanceQueueTransfer interface {
	// ExportGraphMaintenance returns a bank's queued graph-relink unit ids,
	// oldest first.
	ExportGraphMaintenance(ctx context.Context, bankID string) ([]MaintenanceQueueRow, error)
	// ImportGraphMaintenance queues the given graph-relink targets for a bank and
	// reports how many rows actually landed. A target already queued is left
	// alone.
	ImportGraphMaintenance(ctx context.Context, bankID string, rows []MaintenanceQueueRow) (int, error)
	// ExportEntityMaintenance returns a bank's queued entity-prune targets, each
	// named by its canonical name, oldest first.
	ExportEntityMaintenance(ctx context.Context, bankID string) ([]MaintenanceQueueRow, error)
	// ImportEntityMaintenance queues the given entity-prune targets, resolving
	// each name against the target bank, and reports how many rows landed. A name
	// that matches no entity, or a target already queued, is dropped.
	ImportEntityMaintenance(ctx context.Context, bankID string, rows []MaintenanceQueueRow) (int, error)
}

// Postgres store backs the maintenance-queue transfer.
var _ MaintenanceQueueTransfer = (*PostgresStore)(nil)

// ExportGraphMaintenance reads a bank's graph-relink queue.
func (s *PostgresStore) ExportGraphMaintenance(ctx context.Context, bankID string) ([]MaintenanceQueueRow, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	qualified := quoteIdent(schema)
	rows, err := s.db.QueryContext(ctx,
		`SELECT unit_id::text, enqueued_at FROM `+qualified+`.graph_maintenance_queue
		 WHERE bank_id = $1 ORDER BY enqueued_at, unit_id`, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]MaintenanceQueueRow, 0)
	for rows.Next() {
		var row MaintenanceQueueRow
		if err := rows.Scan(&row.ID, &row.EnqueuedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ImportGraphMaintenance queues graph-relink targets for a bank.
func (s *PostgresStore) ImportGraphMaintenance(ctx context.Context, bankID string, rows []MaintenanceQueueRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(rows))
	times := make([]time.Time, 0, len(rows))
	for _, row := range rows {
		if row.ID == "" {
			continue
		}
		ids = append(ids, row.ID)
		times = append(times, row.EnqueuedAt)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return 0, schemaErr
	}
	qualified := quoteIdent(schema)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.graph_maintenance_queue (bank_id, unit_id, enqueued_at)
		 SELECT $1, u::uuid, e FROM unnest($2::uuid[], $3::timestamptz[]) AS t(u, e)
		 ON CONFLICT (bank_id, unit_id) DO NOTHING`, bankID, ids, times)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// ExportEntityMaintenance reads a bank's entity-prune queue, naming each entity
// by its canonical name so the target can re-resolve it. An entity with no name
// (or no row) cannot be restored and is left out.
func (s *PostgresStore) ExportEntityMaintenance(ctx context.Context, bankID string) ([]MaintenanceQueueRow, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	qualified := quoteIdent(schema)
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.canonical_name, q.enqueued_at
		 FROM `+qualified+`.entity_maintenance_queue q
		 JOIN `+qualified+`.entities e ON e.id = q.entity_id
		 WHERE q.bank_id = $1 AND e.canonical_name IS NOT NULL
		 ORDER BY q.enqueued_at, e.canonical_name`, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]MaintenanceQueueRow, 0)
	for rows.Next() {
		var row MaintenanceQueueRow
		if err := rows.Scan(&row.CanonicalName, &row.EnqueuedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ImportEntityMaintenance queues entity-prune targets for a bank, resolving each
// name against the target's own entities.
func (s *PostgresStore) ImportEntityMaintenance(ctx context.Context, bankID string, rows []MaintenanceQueueRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	names := make([]string, 0, len(rows))
	times := make([]time.Time, 0, len(rows))
	for _, row := range rows {
		if row.CanonicalName == "" {
			continue
		}
		names = append(names, row.CanonicalName)
		times = append(times, row.EnqueuedAt)
	}
	if len(names) == 0 {
		return 0, nil
	}
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return 0, schemaErr
	}
	qualified := quoteIdent(schema)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.entity_maintenance_queue (bank_id, entity_id, enqueued_at)
		 SELECT $1, e.id, t.enqueued_at
		 FROM unnest($2::text[], $3::timestamptz[]) AS t(name, enqueued_at)
		 JOIN `+qualified+`.entities e ON e.bank_id = $1 AND e.canonical_name = t.name
		 ON CONFLICT (bank_id, entity_id) DO NOTHING`, bankID, names, times)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// In-process store backs the graph queue it drains; it keeps no entity-prune
// queue, mirroring DrainEntityMaintenance.
var _ MaintenanceQueueTransfer = (*MemoryStore)(nil)

// ExportGraphMaintenance returns the in-process store's queued graph-relink
// targets. The queue carries no enqueue time, so a row's time is its zero value.
func (s *MemoryStore) ExportGraphMaintenance(ctx context.Context, bankID string) ([]MaintenanceQueueRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MaintenanceQueueRow, 0)
	for _, target := range s.relinkQueue {
		if target.BankID == bankID {
			out = append(out, MaintenanceQueueRow{ID: target.ID})
		}
	}
	return out, nil
}

// ImportGraphMaintenance queues graph-relink targets in the in-process store. A
// target already queued is left alone.
func (s *MemoryStore) ImportGraphMaintenance(ctx context.Context, bankID string, rows []MaintenanceQueueRow) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	added := 0
	for _, row := range rows {
		if row.ID == "" {
			continue
		}
		queued := false
		for _, target := range s.relinkQueue {
			if target.BankID == bankID && target.ID == row.ID {
				queued = true
				break
			}
		}
		if queued {
			continue
		}
		s.relinkQueue = append(s.relinkQueue, MaintenanceTarget{BankID: bankID, ID: row.ID})
		added++
	}
	return added, nil
}

// ExportEntityMaintenance reports no rows: the in-process store keeps no
// entity-prune queue.
func (s *MemoryStore) ExportEntityMaintenance(context.Context, string) ([]MaintenanceQueueRow, error) {
	return nil, nil
}

// ImportEntityMaintenance reports no rows queued: the in-process store has no
// entity-prune queue to queue them on.
func (s *MemoryStore) ImportEntityMaintenance(context.Context, string, []MaintenanceQueueRow) (int, error) {
	return 0, nil
}
