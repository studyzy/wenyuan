package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// mentalModelSelect is the shared projection, so every read returns the same
// column order the scanner expects.
const mentalModelSelect = `id, bank_id, subtype, name, COALESCE(description,''), COALESCE(source_query,''),
       COALESCE(content,''), COALESCE(entity_id::text,''), COALESCE(observations, '{}'::jsonb),
       COALESCE(links, '{}')::text, COALESCE(tags, '{}')::text, COALESCE(max_tokens, 2048),
       COALESCE(trigger, '{}'::jsonb), structured_content, reflect_response,
       COALESCE(last_refreshed_source_query, ''),
       last_refreshed_at, last_memory_seen_at, last_refresh_failed_at, last_full_refreshed_at,
       last_updated, created_at`

// backsKnowledgePageSQL tests whether a projected model row holds a knowledge
// page's content: the alias must be `mm`, because the case scans a row that may
// carry no is_knowledge_page column and re-reads the predicate by model id.
const backsKnowledgePageSQL = `EXISTS (SELECT 1 FROM %[1]s.knowledge_pages kp
	WHERE kp.bank_id = mm.bank_id AND kp.mental_model_id = mm.id)`

// ListMentalModels returns the mental models of one bank, newest first.
func (s *PostgresStore) ListMentalModels(ctx context.Context, bankID string, subtype MentalModelSubtype) ([]MentalModel, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []MentalModel
		return zero0, schemaErr
	}
	isPage := fmt.Sprintf(backsKnowledgePageSQL, quoteIdent(schema))
	query := `SELECT ` + mentalModelSelect + `,
	                 CASE WHEN ` + isPage + ` THEN 1 ELSE 0 END
	          FROM ` + quoteIdent(schema) + `.mental_models mm
	          WHERE bank_id = $1 AND ($2 = '' OR subtype = $2)
	          ORDER BY last_refreshed_at DESC, id DESC`
	rows, err := s.reader().QueryContext(ctx, query, bankID, string(subtype))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]MentalModel, 0)
	for rows.Next() {
		model, err := scanMentalModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, model)
	}
	return out, rows.Err()
}

// GetMentalModel returns one mental model by identifier within a bank.
func (s *PostgresStore) GetMentalModel(ctx context.Context, bankID, id string) (MentalModel, error) {
	return s.getMentalModelOn(ctx, s.reader(), bankID, id)
}

// getMentalModelOn reads one model through the given connection, so a read
// serves both a standalone lookup and one inside a caller's transaction.
func (s *PostgresStore) getMentalModelOn(ctx context.Context, q queryer, bankID, id string) (MentalModel, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 MentalModel
		return zero0, schemaErr
	}
	isPage := fmt.Sprintf(backsKnowledgePageSQL, quoteIdent(schema))
	query := `SELECT ` + mentalModelSelect + `,
	                 CASE WHEN ` + isPage + ` THEN 1 ELSE 0 END
	          FROM ` + quoteIdent(schema) + `.mental_models mm
	          WHERE bank_id = $1 AND id = $2`
	model, err := scanMentalModel(q.QueryRowContext(ctx, query, bankID, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MentalModel{}, ErrMentalModelNotFound
		}
		return MentalModel{}, err
	}
	return model, nil
}

// PutMentalModel inserts or replaces a mental model.
func (s *PostgresStore) PutMentalModel(ctx context.Context, model MentalModel) (MentalModel, error) {
	if err := s.putMentalModelOn(ctx, s.db, model); err != nil {
		return MentalModel{}, err
	}
	return s.GetMentalModel(ctx, model.BankID, model.ID)
}

// putMentalModelOn inserts or replaces one model through the given statement
// runner, so one write serves both a standalone put and a put inside a caller's
// transaction.
func (s *PostgresStore) putMentalModelOn(ctx context.Context, e stmtExecer, model MentalModel) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if err := validateMentalModel(model); err != nil {
		return err
	}
	// The reference embeds a mental model from its own text on every write
	// (`_generate_mental_model_embedding(name, content)`, so a create embeds the
	// name and a rename or content change re-embeds), which is what makes a page
	// reachable through the vector arm of knowledge search. A configured
	// embedder therefore owns the column; a caller-supplied vector is kept only
	// when no embedder is configured (an offline or vectored deployment).
	embedding := any(nil)
	if s.observationEmbedder != nil {
		arg, err := mentalModelEmbeddingArg(ctx, s.observationEmbedder, model.Name, model.Content)
		if err != nil {
			return err
		}
		embedding = arg
	} else if len(model.Embedding) > 0 {
		if err := ValidateEmbedding(model.Embedding); err != nil {
			return err
		}
		embedding = NullableEmbedding(model.Embedding)
	}
	observations, err := jsonOrEmpty(model.Observations, `{"observations": []}`)
	if err != nil {
		return err
	}
	trigger, err := jsonOrEmpty(model.Trigger, `{"refresh_after_consolidation": false}`)
	if err != nil {
		return err
	}
	structured, err := jsonOrNull(model.Structured)
	if err != nil {
		return err
	}
	reflectResult, err := jsonOrNull(model.ReflectResult)
	if err != nil {
		return err
	}
	maxTokens := model.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	// The column is NOT NULL DEFAULT now(), so a create is stamped here too: the
	// reference's insert omits the column and lets the default do it, and passing
	// an explicit NULL would fail the constraint instead of taking the default.
	lastRefreshedAt := model.LastRefreshedAt
	if lastRefreshedAt.IsZero() {
		lastRefreshedAt = time.Now().UTC()
	}
	var lastRefreshFailedAt any
	if model.LastRefreshFailedAt != nil {
		lastRefreshFailedAt = *model.LastRefreshFailedAt
	}
	var lastFullRefreshedAt any
	if model.LastFullRefreshedAt != nil {
		lastFullRefreshedAt = *model.LastFullRefreshedAt
	}
	query := `INSERT INTO ` + quoteIdent(schema) + `.mental_models
	          (id, bank_id, subtype, name, description, source_query, content, embedding, entity_id,
	           observations, links, tags, max_tokens, trigger, structured_content, reflect_response,
	           last_refreshed_source_query, last_refreshed_at, last_refresh_failed_at,
	           last_full_refreshed_at, last_updated, created_at)
	          VALUES ($1,$2,$3,$4,$5,$6,$7,$8::vector,NULLIF($9,'')::uuid,$10::jsonb,$11,$12,$13,$14::jsonb,
	                  $15::jsonb,$16::jsonb,$17, $18, $19, $20, now(), now())
	          ON CONFLICT (id, bank_id) DO UPDATE SET
	              subtype = EXCLUDED.subtype, name = EXCLUDED.name, description = EXCLUDED.description,
	              source_query = EXCLUDED.source_query, content = EXCLUDED.content,
	              embedding = COALESCE(EXCLUDED.embedding, ` + quoteIdent(schema) + `.mental_models.embedding),
	              observations = EXCLUDED.observations, links = EXCLUDED.links, tags = EXCLUDED.tags,
	              max_tokens = EXCLUDED.max_tokens, trigger = EXCLUDED.trigger,
	              structured_content = EXCLUDED.structured_content, reflect_response = EXCLUDED.reflect_response,
	              last_refreshed_source_query = EXCLUDED.last_refreshed_source_query,
	              last_refreshed_at = EXCLUDED.last_refreshed_at,
	              last_refresh_failed_at = EXCLUDED.last_refresh_failed_at,
	              last_full_refreshed_at = EXCLUDED.last_full_refreshed_at,
	              last_updated = now()`
	_, err = e.ExecContext(ctx, query, model.ID, model.BankID, string(model.Subtype), model.Name,
		model.Description, model.SourceQuery, model.Content, embedding, model.EntityID,
		observations, model.Links, model.Tags, maxTokens, trigger, structured, reflectResult,
		model.LastRefreshedSourceQuery, lastRefreshedAt, lastRefreshFailedAt, lastFullRefreshedAt)
	return err
}

// UpdateMentalModel applies a partial update and returns the stored value.
func (s *PostgresStore) UpdateMentalModel(ctx context.Context, bankID, id string, patch MentalModelPatch) (MentalModel, error) {
	current, err := s.GetMentalModel(ctx, bankID, id)
	if err != nil {
		return MentalModel{}, err
	}
	applyMentalModelPatch(&current, patch)
	stored, err := s.PutMentalModel(ctx, current)
	if err != nil {
		return MentalModel{}, err
	}
	return withoutKnowledgePageFlag(stored), nil
}

// DeleteMentalModel removes a mental model.
func (s *PostgresStore) DeleteMentalModel(ctx context.Context, bankID, id string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `DELETE FROM ` + quoteIdent(schema) + `.mental_models WHERE bank_id = $1 AND id = $2`
	result, err := s.db.ExecContext(ctx, query, bankID, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrMentalModelNotFound
	}
	return nil
}

// ClearMentalModel empties a mental model's content and history while keeping
// the record.
func (s *PostgresStore) ClearMentalModel(ctx context.Context, bankID, id string) (MentalModel, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 MentalModel
		return zero0, schemaErr
	}
	current, err := s.GetMentalModel(ctx, bankID, id)
	if err != nil {
		return MentalModel{}, err
	}
	// The reference's clear resets exactly content, structured_content,
	// last_refreshed_source_query and last_refresh_failed_at, and rebuilds the
	// dense vector from the name alone: leaving the one built from the discarded
	// content behind would keep the cleared page ranking in semantic recall on a
	// body it no longer has. A deployment with no embedding model writes NULL,
	// which is what the reference's rebuild stores when it has no vector.
	//
	// reflect_response is deliberately not reset — the reference's UPDATE does not
	// name the column, so a cleared model still reports the reflect that produced
	// the content it has since discarded, and a client reading the clear's reply
	// sees the same payload it saw just before (verified against the live service
	// on 2026-10-20). mental_model_history is not touched either: the clear writes
	// no history row on either side.
	embedding, err := mentalModelEmbeddingArg(ctx, s.observationEmbedder, current.Name, "")
	if err != nil {
		return MentalModel{}, err
	}
	query := `UPDATE ` + quoteIdent(schema) + `.mental_models
	          SET content = '', structured_content = NULL,
	              last_refreshed_source_query = NULL, last_refresh_failed_at = NULL,
	              embedding = $3::vector
	          WHERE bank_id = $1 AND id = $2`
	_, err = s.db.ExecContext(ctx, query, bankID, id, embedding)
	if err != nil {
		return MentalModel{}, err
	}
	stored, err := s.GetMentalModel(ctx, bankID, id)
	if err != nil {
		return MentalModel{}, err
	}
	return withoutKnowledgePageFlag(stored), nil
}

// withoutKnowledgePageFlag clears the derived is_knowledge_page flag on a write
// return. The reference's patch and clear queries project their rows without
// the flag and _row_to_mental_model sets the key only when the row carries it,
// so a write answer is null even for a page's backing model (engine/
// memory_engine.py:19620-19624, :19869-19873, :21334-21337); reads keep
// deriving it.
func withoutKnowledgePageFlag(model MentalModel) MentalModel {
	model.IsKnowledgePage = nil
	return model
}

// MentalModelHistory returns the recorded refreshes of one mental model,
// newest first. Each is a row of mental_model_history, which is where the
// reference keeps a change log rather than a column on the model.
func (s *PostgresStore) MentalModelHistory(ctx context.Context, bankID, id string) ([]MentalModelHistoryEntry, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []MentalModelHistoryEntry
		return zero0, schemaErr
	}
	if _, err := s.GetMentalModel(ctx, bankID, id); err != nil {
		return nil, err
	}
	query := `SELECT content, changed_at FROM ` + quoteIdent(schema) + `.mental_model_history
	          WHERE bank_id = $1 AND mental_model_id = $2
	          ORDER BY changed_at DESC, id DESC`
	rows, err := s.reader().QueryContext(ctx, query, bankID, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]MentalModelHistoryEntry, 0)
	for rows.Next() {
		var raw []byte
		var changedAt time.Time
		if err := rows.Scan(&raw, &changedAt); err != nil {
			return nil, err
		}
		entries = append(entries, historyEntryFromMap(decodeJSONMap(raw), changedAt))
	}
	return entries, rows.Err()
}

// RecordMentalModelHistory appends one refresh to a mental model's history.
// The identity is left to the column's default, as the reference leaves it.
func (s *PostgresStore) RecordMentalModelHistory(ctx context.Context, bankID, id string, entry MentalModelHistoryEntry) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if !s.tuning.MentalModelHistory {
		// The reference's switch for the record: a deployment that turned it off
		// keeps no history, and the read routes answer with none.
		return nil
	}
	if _, err := s.GetMentalModel(ctx, bankID, id); err != nil {
		return err
	}
	if entry.ChangedAt.IsZero() {
		entry.ChangedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(entry.Payload())
	if err != nil {
		return err
	}
	query := `INSERT INTO ` + quoteIdent(schema) + `.mental_model_history
	          (mental_model_id, bank_id, content, changed_at)
	          VALUES ($1, $2, $3::jsonb, $4)`
	_, err = s.db.ExecContext(ctx, query, id, bankID, payload, entry.ChangedAt)
	return err
}

// scanMentalModel reads the shared projection.
func scanMentalModel(row rowScanner) (MentalModel, error) {
	var model MentalModel
	var subtype string
	var observations, trigger, structured, reflectResult []byte
	var links, tags string
	var lastRefreshedAt, lastMemorySeenAt, lastRefreshFailedAt, lastFullRefreshedAt, lastUpdated sql.NullTime
	// isKnowledgePage is the CASE projection's 0/1; no other read of this
	// projection selects it, so it is scanned here and never left unset.
	var isKnowledgePage int
	if err := row.Scan(&model.ID, &model.BankID, &subtype, &model.Name, &model.Description,
		&model.SourceQuery, &model.Content, &model.EntityID, &observations,
		&links, &tags, &model.MaxTokens, &trigger, &structured, &reflectResult,
		&model.LastRefreshedSourceQuery, &lastRefreshedAt, &lastMemorySeenAt, &lastRefreshFailedAt,
		&lastFullRefreshedAt, &lastUpdated, &model.CreatedAt, &isKnowledgePage); err != nil {
		return MentalModel{}, err
	}
	model.Subtype = MentalModelSubtype(subtype)
	model.Links = decodeTextArray(links)
	model.Tags = decodeTextArray(tags)
	model.LastRefreshedAt = derefTime(lastRefreshedAt)
	model.LastMemorySeenAt = nullTimePtr(lastMemorySeenAt)
	model.LastRefreshFailedAt = nullTimePtr(lastRefreshFailedAt)
	model.LastFullRefreshedAt = nullTimePtr(lastFullRefreshedAt)
	backed := isKnowledgePage != 0
	model.IsKnowledgePage = &backed
	model.LastUpdated = derefTime(lastUpdated)
	model.Observations = decodeJSONMap(observations)
	model.Trigger = decodeJSONMap(trigger)
	model.Structured = decodeJSONMap(structured)
	model.ReflectResult = decodeJSONMap(reflectResult)
	return model, nil
}

// jsonOrEmpty encodes a map, substituting a literal when it is nil.
func jsonOrEmpty(value map[string]any, empty string) ([]byte, error) {
	if value == nil {
		return []byte(empty), nil
	}
	return json.Marshal(value)
}

// jsonOrNull encodes a map, yielding SQL NULL when it is nil.
func jsonOrNull(value map[string]any) (any, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

// decodeJSONMap decodes a jsonb object, tolerating null.
func decodeJSONMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// derefTime returns the time or the zero value.
func derefTime(value sql.NullTime) time.Time {
	if value.Valid {
		return value.Time
	}
	return time.Time{}
}

// historyEntryToMap renders a history entry for storage, under the reference's
// keys: the content and reflect response a refresh replaced, plus the failure
// fields a refused write records.
func historyEntryToMap(entry MentalModelHistoryEntry) map[string]any {
	out := map[string]any{
		"previous_content":          nil,
		"previous_reflect_response": entry.PreviousReflectResponse,
	}
	if entry.PreviousContent != nil {
		out["previous_content"] = *entry.PreviousContent
	}
	if entry.Kind != "" {
		out["kind"] = entry.Kind
		out["outcome"] = entry.Outcome
		out["failure_reason"] = entry.FailureReason
		out["error_message"] = entry.ErrorMessage
	}
	return out
}

// historyEntryFromMap reads a history entry back from storage. changedAt is the
// row's own column, not part of the JSON blob.
func historyEntryFromMap(raw map[string]any, changedAt time.Time) MentalModelHistoryEntry {
	entry := MentalModelHistoryEntry{ChangedAt: changedAt}
	if value, ok := raw["previous_content"].(string); ok {
		entry.PreviousContent = &value
	}
	if value, ok := raw["previous_reflect_response"].(map[string]any); ok {
		entry.PreviousReflectResponse = value
	}
	if value, ok := raw["kind"].(string); ok {
		entry.Kind = value
	}
	if value, ok := raw["outcome"].(string); ok {
		entry.Outcome = value
	}
	if value, ok := raw["failure_reason"].(string); ok {
		entry.FailureReason = value
	}
	if value, ok := raw["error_message"].(string); ok {
		entry.ErrorMessage = value
	}
	return entry
}

// normalizeTags returns a non-nil tag slice, because the column is not null.
func normalizeTags(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// PromoteOperationPayload sets one payload member on an operation that is still
// in the expected status, so a queued refresh can be upgraded to a full one
// without changing what it works on. The status is part of the statement rather
// than a check in front of it, because the row may be claimed between the read
// and the write: a run that has started has already chosen its mode.
func (s *PostgresStore) PromoteOperationPayload(ctx context.Context, bankID, operationID, key string, value any, onlyStatus OperationStatus) (bool, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return false, schemaErr
	}
	encoded, err := json.Marshal(map[string]any{key: value})
	if err != nil {
		return false, err
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET task_payload = (COALESCE(task_payload, '{}'::jsonb) || $4::jsonb),
	              updated_at = now()
	          WHERE operation_id = $1 AND bank_id = $2 AND status = $3
	            AND COALESCE(task_payload ->> $5, '') <> $6`
	result, err := s.db.ExecContext(ctx, query, operationID, bankID, string(onlyStatus), encoded, key, value)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// ClearOperationRefreshPark unparks a pending refresh: the automatic marker
// leaves the payload and next_retry_at clears in the same write.
func (s *PostgresStore) ClearOperationRefreshPark(ctx context.Context, bankID, operationID string) (bool, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return false, schemaErr
	}
	query := `UPDATE ` + quoteIdent(schema) + `.async_operations
	          SET task_payload = task_payload - $4,
	              next_retry_at = NULL,
	              updated_at = now()
	          WHERE operation_id = $1 AND bank_id = $2 AND status = $3
	            AND (task_payload ? $4 OR next_retry_at IS NOT NULL)`
	result, err := s.db.ExecContext(ctx, query, operationID, bankID, string(StatusPending), AutomaticRefreshPayloadKey)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// WithOperationSubjectLock serializes two submits that decide on the same
// operation subject.
//
// It uses a transaction-scoped advisory lock rather than a row lock on the
// bank: the callback queues the operation through EnqueueOperation, which opens
// its own transaction and takes the bank row's FOR NO KEY UPDATE lock. Holding
// that same row lock here as well would make the callback wait on a lock its
// own caller holds — a self-deadlock that hangs the refresh request forever.
func (s *PostgresStore) WithOperationSubjectLock(ctx context.Context, bankID, operationType string, fn func() error) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`,
		schema+":"+bankID, operationType); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	}
	return tx.Commit()
}
