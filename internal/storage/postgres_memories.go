package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ListMemories returns one page of a bank's memory units, newest first.
func (s *PostgresStore) ListMemories(ctx context.Context, query MemoryQuery) (MemoryPage, error) {
	filter := `bank_id = $1 AND ($2 = '' OR type = $2)`
	qualified := quoteIdent(s.schema)
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.memory_units WHERE `+filter,
		query.BankID, query.Type).Scan(&total); err != nil {
		return MemoryPage{}, err
	}
	limit := clampLimit(query.Limit, 50)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, bank_id, COALESCE(text,''), COALESCE(type,''), COALESCE(context,''),
		        occurred_start, occurred_end, mentioned_at, event_date,
		        COALESCE(document_id,''), COALESCE(chunk_id,''), COALESCE(tags, '{}'),
		        COALESCE(metadata, '{}'::jsonb)
		 FROM `+qualified+`.memory_units WHERE `+filter+`
		 ORDER BY mentioned_at DESC NULLS LAST, id ASC LIMIT $3 OFFSET $4`,
		query.BankID, query.Type, limit, query.Offset)
	if err != nil {
		return MemoryPage{}, err
	}
	defer func() { _ = rows.Close() }()
	units := make([]MemoryUnit, 0)
	for rows.Next() {
		unit, err := scanMemoryUnitRow(rows)
		if err != nil {
			return MemoryPage{}, err
		}
		units = append(units, unit)
	}
	return MemoryPage{Memories: units, Total: total, Limit: query.Limit, Offset: query.Offset}, rows.Err()
}

// GetMemory returns one memory unit by identifier.
func (s *PostgresStore) GetMemory(ctx context.Context, bankID, memoryID string) (MemoryUnit, error) {
	qualified := quoteIdent(s.schema)
	query := `SELECT id, bank_id, COALESCE(text,''), COALESCE(type,''), COALESCE(context,''),
	                 occurred_start, occurred_end, mentioned_at, event_date,
	                 COALESCE(document_id,''), COALESCE(chunk_id,''), COALESCE(tags, '{}'),
	                 COALESCE(metadata, '{}'::jsonb)
	          FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND id = $2`
	unit, err := scanMemoryUnitRow(s.db.QueryRowContext(ctx, query, bankID, memoryID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MemoryUnit{}, ErrMemoryNotFound
		}
		return MemoryUnit{}, err
	}
	return unit, nil
}

// UpdateMemory applies a partial update to one memory unit.
func (s *PostgresStore) UpdateMemory(ctx context.Context, bankID, memoryID string, patch MemoryPatch) (MemoryUnit, error) {
	current, err := s.GetMemory(ctx, bankID, memoryID)
	if err != nil {
		return MemoryUnit{}, err
	}
	if patch.Text != nil {
		current.Text = *patch.Text
	}
	if patch.Tags != nil {
		current.Tags = append([]string(nil), (*patch.Tags)...)
	}
	if patch.Metadata != nil {
		current.Metadata = copyAnyMap(*patch.Metadata)
	}
	qualified := quoteIdent(s.schema)
	metadata, err := jsonColumn(current.Metadata)
	if err != nil {
		return MemoryUnit{}, err
	}
	query := `UPDATE ` + qualified + `.memory_units
	          SET text = $3, tags = $4, metadata = $5::jsonb
	          WHERE bank_id = $1 AND id = $2`
	if _, err := s.db.ExecContext(ctx, query, bankID, memoryID,
		current.Text, normalizeTags(current.Tags), metadata); err != nil {
		return MemoryUnit{}, err
	}
	// The change is recorded so a memory's history explains its current state.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.audit_log (id, action, transport, bank_id, request, metadata)
		 VALUES ($1, 'memory_update', 'http', $2, $3::jsonb, '{}'::jsonb)`,
		newID(), bankID, mustJSON(map[string]any{"memory_id": memoryID})); err != nil {
		// A missing audit trail must not fail the update itself.
		_ = err
	}
	return s.GetMemory(ctx, bankID, memoryID)
}

// MemoryHistory returns the recorded changes of one memory unit, newest first.
//
// The history is read from the audit trail, because every update records
// itself there rather than keeping a second copy of the same facts.
func (s *PostgresStore) MemoryHistory(ctx context.Context, bankID, memoryID string) ([]MemoryHistoryEntry, error) {
	if _, err := s.GetMemory(ctx, bankID, memoryID); err != nil {
		return nil, err
	}
	qualified := quoteIdent(s.schema)
	rows, err := s.db.QueryContext(ctx,
		`SELECT started_at, COALESCE(metadata, '{}'::jsonb), COALESCE(request, 'null'::jsonb)
		 FROM `+qualified+`.audit_log
		 WHERE bank_id = $1 AND action = 'memory_update' AND request->>'memory_id' = $2
		 ORDER BY started_at DESC`,
		bankID, memoryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]MemoryHistoryEntry, 0)
	for rows.Next() {
		var entry MemoryHistoryEntry
		var metadata, request []byte
		if err := rows.Scan(&entry.ChangedAt, &metadata, &request); err != nil {
			return nil, err
		}
		entry.Reason = "update"
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// DeleteObservations removes the observations of a bank, or of one memory.
func (s *PostgresStore) DeleteObservations(ctx context.Context, bankID, memoryID string) (int, error) {
	qualified := quoteIdent(s.schema)
	query := `DELETE FROM ` + qualified + `.memory_units
	          WHERE bank_id = $1 AND type = 'observation'
	            AND ($2 = '' OR id IN (SELECT observation_id FROM ` + qualified + `.observation_sources WHERE source_id = $2))`
	result, err := s.db.ExecContext(ctx, query, bankID, memoryID)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// EnqueueConsolidation records a consolidation pass for a bank.
func (s *PostgresStore) EnqueueConsolidation(ctx context.Context, bankID string) (Operation, bool, error) {
	// A second consolidation while one is outstanding would do the same work
	// twice, so an existing one is returned instead of a new one.
	existing, err := s.findPendingConsolidation(ctx, bankID)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, ErrOperationNotFound) {
		return Operation{}, false, err
	}
	op, err := s.PutOperation(ctx, Operation{
		ID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending,
	})
	if err != nil {
		return Operation{}, false, err
	}
	return op, false, nil
}

// findPendingConsolidation returns a non-terminal consolidation for a bank.
func (s *PostgresStore) findPendingConsolidation(ctx context.Context, bankID string) (Operation, error) {
	qualified := quoteIdent(s.schema)
	query := `SELECT id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''), run,
	                 created_at, updated_at, completed_at, COALESCE(retry_count,0), next_retry_at,
	                 result_metadata, COALESCE(worker_id,''), claimed_at, COALESCE(task_payload, 'null'::jsonb), COALESCE(serialization_key,'')
	          FROM ` + qualified + `.async_operations
	          WHERE bank_id = $1 AND operation_type = $2 AND status IN ('pending','processing')
	          ORDER BY created_at ASC LIMIT 1`
	op, err := scanOperation(s.db.QueryRowContext(ctx, query, bankID, OperationTypeConsolidate))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Operation{}, ErrOperationNotFound
		}
		return Operation{}, err
	}
	return op, nil
}

// RecoverConsolidations retries a bank's failed consolidations.
func (s *PostgresStore) RecoverConsolidations(ctx context.Context, bankID string) (int, error) {
	qualified := quoteIdent(s.schema)
	query := `UPDATE ` + qualified + `.async_operations
	          SET status = 'pending', error_message = '', next_retry_at = NULL,
	              worker_id = NULL, claimed_at = NULL, updated_at = now()
	          WHERE bank_id = $1 AND operation_type = $2 AND status = 'failed'`
	result, err := s.db.ExecContext(ctx, query, bankID, OperationTypeConsolidate)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

// scanMemoryUnitRow reads the memory-unit projection.
func scanMemoryUnitRow(row rowScanner) (MemoryUnit, error) {
	var unit MemoryUnit
	var unitType string
	var occurredStart, occurredEnd, mentionedAt, eventDate sql.NullTime
	var tagsRaw, metadataRaw []byte
	if err := row.Scan(&unit.ID, &unit.BankID, &unit.Text, &unitType, &unit.Context,
		&occurredStart, &occurredEnd, &mentionedAt, &eventDate,
		&unit.DocumentID, &unit.ChunkID, &tagsRaw, &metadataRaw); err != nil {
		return MemoryUnit{}, err
	}
	unit.Type = MemoryUnitType(unitType)
	unit.OccurredStart = nullTimePtr(occurredStart)
	unit.OccurredEnd = nullTimePtr(occurredEnd)
	unit.MentionedAt = nullTimePtr(mentionedAt)
	unit.EventDate = nullTimePtr(eventDate)
	unit.Tags = decodePostgresTextArray(tagsRaw)
	unit.Metadata = decodeJSONMap(metadataRaw)
	return unit, nil
}

// mustJSON renders a value for a jsonb column, substituting an empty object on
// a failure so a diagnostic insert cannot fail on its own payload.
func mustJSON(value map[string]any) []byte {
	encoded, err := jsonColumn(value)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// ensure strings is used by the knowledge search.
var _ = strings.TrimSpace
