package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// ListMemories returns one page of a bank's memory units, newest first. A
// state of "invalidated" reads the archive table instead of the live one.
func (s *PostgresStore) ListMemories(ctx context.Context, query MemoryQuery) (MemoryPage, error) {
	qualified := quoteIdent(s.schema)
	table := qualified + `.memory_units`
	invalidatedCols := `NULL::timestamptz, NULL::text`
	if query.State == "invalidated" {
		table = qualified + `.invalidated_memory_units`
		invalidatedCols = `mu.invalidated_at, mu.invalidation_reason`
	}
	filter := `mu.bank_id = $1
	           AND ($2 = '' OR mu.fact_type = $2)
	           AND ($3 = '' OR mu.text ILIKE '%' || $3 || '%')
	           AND ($4 = '' OR mu.document_id = $4)
	           AND ($5 = '' OR EXISTS (SELECT 1 FROM ` + qualified + `.unit_entities ue
	                                    WHERE ue.unit_id = mu.id AND ue.entity_id = $5::uuid))
	           AND ($6::text[] IS NULL OR ` + tagExprFor(query.TagsMatch) + `)
	           AND ($7 = '' OR ($7 = 'failed' AND mu.consolidation_failed_at IS NOT NULL)
	                        OR ($7 = 'pending' AND mu.consolidation_failed_at IS NULL AND mu.consolidated_at IS NULL)
	                        OR ($7 = 'done' AND mu.consolidated_at IS NOT NULL))
	           AND ($8::timestamptz IS NULL OR mu.` + timeColumnFor(query.TimeField) + ` >= $8)
	           AND ($9::timestamptz IS NULL OR mu.` + timeColumnFor(query.TimeField) + ` <= $9)`
	// Naming a time axis excludes the rows with no value on it, so the ordering
	// it drives is total. A query that named no axis keeps the historical
	// default ordering and no exclusion.
	timeColumn := timeColumnFor(query.TimeField)
	if query.TimeField != "" {
		filter += ` AND mu.` + timeColumn + ` IS NOT NULL`
	}
	tags := nullableTagArray(query.Tags)
	args := []any{query.BankID, query.Type, strings.TrimSpace(query.Q), query.DocumentID,
		query.EntityID, tags, query.ConsolidationState, query.TimeStart, query.TimeEnd}
	// A compound filter — the caller's resolved read scope — is AND-ed in as an
	// extra predicate, so a scoped reader pages over the memories it may read
	// without replacing the request's own tag filter. The placeholders continue
	// past the fixed ones, so LIMIT/OFFSET are addressed after whatever it added.
	if groupClause, groupArgs := renderTagGroups(query.TagGroups, len(args)); groupClause != "" {
		filter += ` AND (` + groupClause + `)`
		args = append(args, groupArgs...)
	}
	limitParam := "$" + itoa(len(args)+1)
	offsetParam := "$" + itoa(len(args)+2)
	limit := clampLimit(query.Limit, 50)
	var total int
	if err := s.reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+table+` mu WHERE `+filter, args...).Scan(&total); err != nil {
		return MemoryPage{}, err
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT mu.id, mu.bank_id, COALESCE(mu.text,''), COALESCE(mu.fact_type,''), COALESCE(mu.context,''),
		        mu.occurred_start, mu.occurred_end, mu.mentioned_at, mu.event_date,
		        COALESCE(mu.document_id,''), COALESCE(mu.chunk_id,''), COALESCE(mu.tags, '{}'),
		        COALESCE(mu.metadata, '{}'::jsonb), mu.created_at, mu.updated_at, mu.edited_at,
		        COALESCE(mu.proof_count, 1), COALESCE(mu.source_memory_ids, '{}'::uuid[])::text,
		        mu.consolidated_at, mu.consolidation_failed_at,
		        COALESCE(mu.observation_scopes, 'null'::jsonb)::text, `+invalidatedCols+`
		 FROM `+table+` mu WHERE `+filter+`
		 ORDER BY mu.`+timeColumn+` DESC NULLS LAST, mu.id ASC LIMIT `+limitParam+` OFFSET `+offsetParam,
		append(args, limit, query.Offset)...)
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

// tagExprFor maps a tag-match mode to the SQL predicate used against the $6
// tag array parameter.
func tagExprFor(mode string) string {
	switch mode {
	case TagsMatchAll, TagsMatchAllStrict:
		return `tags::text[] @> $6`
	case TagsMatchExact:
		return `(tags::text[] <@ $6 AND tags::text[] @> $6)`
	default: // any, any_strict and the empty default: overlap.
		return `tags::text[] && $6`
	}
}

// timeColumnFor maps a time-field selector to a column, from a closed set so
// the name is never interpolated from caller input.
func timeColumnFor(field string) string {
	switch field {
	case "created_at":
		return "created_at"
	case "updated_at":
		return "updated_at"
	case "occurred_start":
		return "occurred_start"
	case "occurred_end":
		return "occurred_end"
	default:
		return "mentioned_at"
	}
}

// GetMemory returns one memory unit by identifier, reading the archive table
// when the unit has been invalidated.
func (s *PostgresStore) GetMemory(ctx context.Context, bankID, memoryID string) (MemoryUnit, error) {
	qualified := quoteIdent(s.schema)
	cols := `id, bank_id, COALESCE(text,''), COALESCE(fact_type,''), COALESCE(context,''),
	                 occurred_start, occurred_end, mentioned_at, event_date,
	                 COALESCE(document_id,''), COALESCE(chunk_id,''), COALESCE(tags, '{}'),
	                 COALESCE(metadata, '{}'::jsonb), created_at, updated_at, edited_at,
	                 COALESCE(proof_count, 1), COALESCE(source_memory_ids, '{}'::uuid[])::text,
	                 consolidated_at, consolidation_failed_at,
	                 COALESCE(observation_scopes, 'null'::jsonb)::text`
	unit, err := scanMemoryUnitRow(s.reader().QueryRowContext(ctx,
		`SELECT `+cols+`, NULL::timestamptz, NULL::text
		 FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = $2`, bankID, memoryID))
	if err == nil {
		return unit, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MemoryUnit{}, err
	}
	unit, err = scanMemoryUnitRow(s.reader().QueryRowContext(ctx,
		`SELECT `+cols+`, invalidated_at, invalidation_reason
		 FROM `+qualified+`.invalidated_memory_units WHERE bank_id = $1 AND id = $2`, bankID, memoryID))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryUnit{}, ErrMemoryNotFound
	}
	return unit, err
}

// UpdateMemory applies a partial update to one memory unit. A state of
// "invalidated" moves the row to the archive (cascading its links and entity
// associations); "valid" moves it back.
func (s *PostgresStore) UpdateMemory(ctx context.Context, bankID, memoryID string, patch MemoryPatch) (MemoryUnit, error) {
	current, err := s.GetMemory(ctx, bankID, memoryID)
	if err != nil {
		return MemoryUnit{}, err
	}
	qualified := quoteIdent(s.schema)
	if patch.State != nil {
		switch *patch.State {
		case "invalidated":
			if err := s.invalidateMemory(ctx, bankID, memoryID, patch.Reason); err != nil {
				return MemoryUnit{}, err
			}
			return s.GetMemory(ctx, bankID, memoryID)
		case "valid":
			if err := s.revertMemory(ctx, bankID, memoryID); err != nil {
				return MemoryUnit{}, err
			}
			return s.GetMemory(ctx, bankID, memoryID)
		}
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
	if patch.Context != nil {
		current.Context = *patch.Context
	}
	if patch.FactType != nil {
		current.FactType = MemoryUnitType(*patch.FactType)
	}
	current.OccurredStart = applyDatePatch(current.OccurredStart, patch.OccurredStart)
	current.OccurredEnd = applyDatePatch(current.OccurredEnd, patch.OccurredEnd)
	// The entity set is a relation rather than a column, so it is rewritten
	// through its own path instead of the UPDATE below.
	if patch.Entities != nil {
		resolve := patch.ResolveEntities == nil || *patch.ResolveEntities
		if err := s.SetUnitEntities(ctx, bankID, memoryID, *patch.Entities, resolve); err != nil {
			return MemoryUnit{}, err
		}
	}
	metadata, err := jsonColumn(current.Metadata)
	if err != nil {
		return MemoryUnit{}, err
	}
	query := `UPDATE ` + qualified + `.memory_units
	          SET text = $3, search_vector = ` + searchVectorExpr("$3", "$6", "text_signals") + `, tags = $4, metadata = $5::jsonb,
	              context = $6, fact_type = $7, occurred_start = $8, occurred_end = $9, edited_at = now()
	          WHERE bank_id = $1 AND id = $2`
	if _, err := s.db.ExecContext(ctx, query, bankID, memoryID,
		current.Text, normalizeTags(current.Tags), metadata, current.Context,
		string(current.FactType), current.OccurredStart, current.OccurredEnd); err != nil {
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

// curationColumns are the memory-unit columns that survive a move between the
// live table and the invalidation archive. embedding and text_signals do not,
// matching the reference, which drops them on invalidation.
const curationColumns = `id, bank_id, document_id, text, context, event_date, occurred_start, occurred_end,
	mentioned_at, fact_type, metadata, created_at, updated_at, chunk_id, tags, proof_count,
	source_memory_ids, consolidated_at, observation_scopes, consolidation_failed_at, edited_at, attachment_ids`

// invalidateMemory moves a live unit to the archive table, cascading its links
// and entity associations.
func (s *PostgresStore) invalidateMemory(ctx context.Context, bankID, memoryID string, reason *string) error {
	qualified := quoteIdent(s.schema)
	var reasonArg any
	if reason != nil {
		reasonArg = *reason
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.invalidated_memory_units (`+curationColumns+`, invalidation_reason, invalidated_at)
		 SELECT `+curationColumns+`, $3, now()
		 FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = $2
		 ON CONFLICT (id) DO NOTHING`, bankID, memoryID, reasonArg); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_links WHERE bank_id = $1 AND (from_unit_id = $2 OR to_unit_id = $2)`,
		bankID, memoryID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.unit_entities WHERE unit_id = $1`, memoryID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = $2`, bankID, memoryID)
	return err
}

// revertMemory moves an archived unit back to the live table and clears its
// invalidation fields. Its embedding is not restored; the reference recomputes
// it rather than keeping a cold copy.
func (s *PostgresStore) revertMemory(ctx context.Context, bankID, memoryID string) error {
	qualified := quoteIdent(s.schema)
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.memory_units (`+curationColumns+`)
		 SELECT `+curationColumns+` FROM `+qualified+`.invalidated_memory_units
		 WHERE bank_id = $1 AND id = $2
		 ON CONFLICT (id) DO NOTHING`, bankID, memoryID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.invalidated_memory_units WHERE bank_id = $1 AND id = $2`, bankID, memoryID)
	return err
}

// MemoryHistory returns the recorded changes of one memory unit, newest first.
//
// The history is read from the audit trail, because every update records
// itself there rather than keeping a second copy of the same facts. An
// observation's consolidation rewrites are not audit actions, so its own
// observation_history rows are merged in: they are what make "what did this
// conclusion say before it was reinforced" answerable.
func (s *PostgresStore) MemoryHistory(ctx context.Context, bankID, memoryID string) ([]MemoryHistoryEntry, error) {
	if _, err := s.GetMemory(ctx, bankID, memoryID); err != nil {
		return nil, err
	}
	qualified := quoteIdent(s.schema)
	rows, err := s.reader().QueryContext(ctx,
		`SELECT started_at, COALESCE(metadata, '{}'::jsonb), COALESCE(request, 'null'::jsonb)
		 FROM `+qualified+`.audit_log
		 WHERE bank_id = $1 AND action = 'memory_update' AND request->>'memory_id' = $2
		 ORDER BY started_at DESC`,
		bankID, memoryID)
	if err != nil {
		return nil, err
	}
	entries := make([]MemoryHistoryEntry, 0)
	for rows.Next() {
		var entry MemoryHistoryEntry
		var metadata, request []byte
		if err := rows.Scan(&entry.ChangedAt, &metadata, &request); err != nil {
			_ = rows.Close()
			return nil, err
		}
		entry.Reason = "update"
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	snapshots, err := s.observationHistoryEntries(ctx, bankID, memoryID)
	if err != nil {
		return nil, err
	}
	entries = append(entries, snapshots...)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].ChangedAt.After(entries[j].ChangedAt)
	})
	return entries, nil
}

// observationHistoryEntries reads an observation's pre-update snapshots, newest
// first.
func (s *PostgresStore) observationHistoryEntries(ctx context.Context, bankID, memoryID string) ([]MemoryHistoryEntry, error) {
	query := `SELECT COALESCE(content->>'text',''), changed_at
	          FROM ` + quoteIdent(s.schema) + `.observation_history
	          WHERE bank_id = $1 AND observation_id = $2
	          ORDER BY changed_at DESC, id DESC`
	rows, err := s.reader().QueryContext(ctx, query, bankID, memoryID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var entries []MemoryHistoryEntry
	for rows.Next() {
		entry := MemoryHistoryEntry{Reason: "consolidated"}
		if err := rows.Scan(&entry.Text, &entry.ChangedAt); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// DeleteObservations removes the observations of a bank, or of one memory.
// An observation is tied to a memory by carrying it as evidence, so the filter
// is an overlap test against the observation's source_memory_ids array.
func (s *PostgresStore) DeleteObservations(ctx context.Context, bankID, memoryID string) (int, error) {
	qualified := quoteIdent(s.schema)
	query := `DELETE FROM ` + qualified + `.memory_units
	          WHERE bank_id = $1 AND fact_type = 'observation'
	            AND ($2 = '' OR source_memory_ids && ARRAY[$2::uuid])`
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
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, SerializationKey: ConsolidationSerializationKey(bankID),
	})
	if err != nil {
		return Operation{}, false, err
	}
	return op, false, nil
}

// findPendingConsolidation returns a non-terminal consolidation for a bank.
func (s *PostgresStore) findPendingConsolidation(ctx context.Context, bankID string) (Operation, error) {
	qualified := quoteIdent(s.schema)
	query := `SELECT operation_id, bank_id, COALESCE(operation_type,''), status, COALESCE(error_message,''),
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

// RecoverConsolidations returns a bank's permanently-failed sources to the
// not-yet-consolidated state and reports how many. The count is per stranded
// fact, not per round. A pass is queued so the recovery lands without waiting
// for the next reconcile sweep.
func (s *PostgresStore) RecoverConsolidations(ctx context.Context, bankID string) (int, error) {
	qualified := quoteIdent(s.schema)
	query := `WITH requeued AS (
	              UPDATE ` + qualified + `.memory_units
	              SET consolidation_failed_at = NULL, consolidated_at = NULL
	              WHERE bank_id = $1 AND consolidation_failed_at IS NOT NULL
	              RETURNING 1
	          )
	          SELECT count(*) FROM requeued`
	var retried int
	if err := s.db.QueryRowContext(ctx, query, bankID).Scan(&retried); err != nil {
		return 0, err
	}
	if retried == 0 {
		return 0, nil
	}
	if _, _, err := s.EnqueueConsolidation(ctx, bankID); err != nil {
		return retried, err
	}
	return retried, nil
}

// scanMemoryUnitRow reads the memory-unit projection.
func scanMemoryUnitRow(row rowScanner) (MemoryUnit, error) {
	var unit MemoryUnit
	var unitType string
	var occurredStart, occurredEnd, mentionedAt, eventDate sql.NullTime
	var createdAt, updatedAt, editedAt, invalidatedAt sql.NullTime
	var consolidatedAt, consolidationFailedAt sql.NullTime
	var invalidationReason sql.NullString
	var sourceIDsRaw, scopesRaw sql.NullString
	var tagsRaw, metadataRaw []byte
	if err := row.Scan(&unit.ID, &unit.BankID, &unit.Text, &unitType, &unit.Context,
		&occurredStart, &occurredEnd, &mentionedAt, &eventDate,
		&unit.DocumentID, &unit.ChunkID, &tagsRaw, &metadataRaw,
		&createdAt, &updatedAt, &editedAt,
		&unit.ProofCount, &sourceIDsRaw, &consolidatedAt, &consolidationFailedAt, &scopesRaw,
		&invalidatedAt, &invalidationReason); err != nil {
		return MemoryUnit{}, err
	}
	unit.FactType = MemoryUnitType(unitType)
	unit.OccurredStart = nullTimePtr(occurredStart)
	unit.OccurredEnd = nullTimePtr(occurredEnd)
	unit.MentionedAt = nullTimePtr(mentionedAt)
	unit.EventDate = nullTimePtr(eventDate)
	unit.Tags = decodePostgresTextArray(tagsRaw)
	unit.Metadata = decodeJSONMap(metadataRaw)
	unit.CreatedAt = nullTimePtr(createdAt)
	unit.UpdatedAt = nullTimePtr(updatedAt)
	unit.EditedAt = nullTimePtr(editedAt)
	unit.SourceMemoryIDs = decodePostgresTextArray([]byte(sourceIDsRaw.String))
	unit.ConsolidatedAt = nullTimePtr(consolidatedAt)
	unit.ConsolidationFailedAt = nullTimePtr(consolidationFailedAt)
	if scopesRaw.Valid && scopesRaw.String != "" && scopesRaw.String != "null" {
		unit.ObservationScopes = json.RawMessage(scopesRaw.String)
	}
	unit.InvalidatedAt = nullTimePtr(invalidatedAt)
	if invalidationReason.Valid {
		reason := invalidationReason.String
		unit.InvalidationReason = &reason
	}
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
