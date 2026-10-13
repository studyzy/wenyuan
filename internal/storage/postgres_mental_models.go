package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// mentalModelSelect is the shared projection, so every read returns the same
// column order the scanner expects.
const mentalModelSelect = `id, bank_id, subtype, name, COALESCE(description,''), COALESCE(source_query,''),
       COALESCE(content,''), COALESCE(entity_id::text,''), COALESCE(observations, '{}'::jsonb),
       COALESCE(links, '{}')::text, COALESCE(tags, '{}')::text, COALESCE(max_tokens, 2048),
       COALESCE(trigger, '{}'::jsonb), structured_content, reflect_response,
       COALESCE(history, '[]'::jsonb), COALESCE(last_refreshed_source_query, ''),
       last_refreshed_at, last_updated, created_at`

// ListMentalModels returns the mental models of one bank, newest first.
func (s *PostgresStore) ListMentalModels(ctx context.Context, bankID string, subtype MentalModelSubtype) ([]MentalModel, error) {
	query := `SELECT ` + mentalModelSelect + ` FROM ` + quoteIdent(s.schema) + `.mental_models
	          WHERE bank_id = $1 AND ($2 = '' OR subtype = $2)
	          ORDER BY created_at DESC, id ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID, string(subtype))
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
	query := `SELECT ` + mentalModelSelect + ` FROM ` + quoteIdent(s.schema) + `.mental_models
	          WHERE bank_id = $1 AND id = $2`
	model, err := scanMentalModel(s.db.QueryRowContext(ctx, query, bankID, id))
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
	if err := validateMentalModel(model); err != nil {
		return MentalModel{}, err
	}
	embedding := any(nil)
	if len(model.Embedding) > 0 {
		if err := ValidateEmbedding(model.Embedding); err != nil {
			return MentalModel{}, err
		}
		embedding = NullableEmbedding(model.Embedding)
	}
	observations, err := jsonOrEmpty(model.Observations, `{"observations": []}`)
	if err != nil {
		return MentalModel{}, err
	}
	trigger, err := jsonOrEmpty(model.Trigger, `{"refresh_after_consolidation": false}`)
	if err != nil {
		return MentalModel{}, err
	}
	structured, err := jsonOrNull(model.Structured)
	if err != nil {
		return MentalModel{}, err
	}
	reflectResult, err := jsonOrNull(model.ReflectResult)
	if err != nil {
		return MentalModel{}, err
	}
	history, err := jsonHistory(model.History)
	if err != nil {
		return MentalModel{}, err
	}
	maxTokens := model.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.mental_models
	          (id, bank_id, subtype, name, description, source_query, content, embedding, entity_id,
	           observations, links, tags, max_tokens, trigger, structured_content, reflect_response,
	           history, last_refreshed_source_query, last_refreshed_at, last_updated, created_at)
	          VALUES ($1,$2,$3,$4,$5,$6,$7,$8::vector,NULLIF($9,'')::uuid,$10::jsonb,$11,$12,$13,$14::jsonb,
	                  $15::jsonb,$16::jsonb,$17::jsonb,$18, now(), now(), now())
	          ON CONFLICT (id, bank_id) DO UPDATE SET
	              subtype = EXCLUDED.subtype, name = EXCLUDED.name, description = EXCLUDED.description,
	              source_query = EXCLUDED.source_query, content = EXCLUDED.content,
	              embedding = COALESCE(EXCLUDED.embedding, ` + quoteIdent(s.schema) + `.mental_models.embedding),
	              observations = EXCLUDED.observations, links = EXCLUDED.links, tags = EXCLUDED.tags,
	              max_tokens = EXCLUDED.max_tokens, trigger = EXCLUDED.trigger,
	              structured_content = EXCLUDED.structured_content, reflect_response = EXCLUDED.reflect_response,
	              history = EXCLUDED.history,
	              last_refreshed_source_query = EXCLUDED.last_refreshed_source_query,
	              last_updated = now()`
	_, err = s.db.ExecContext(ctx, query, model.ID, model.BankID, string(model.Subtype), model.Name,
		model.Description, model.SourceQuery, model.Content, embedding, model.EntityID,
		observations, model.Links, model.Tags, maxTokens, trigger, structured, reflectResult,
		history, model.LastRefreshedSourceQuery)
	if err != nil {
		return MentalModel{}, err
	}
	return s.GetMentalModel(ctx, model.BankID, model.ID)
}

// UpdateMentalModel applies a partial update and returns the stored value.
func (s *PostgresStore) UpdateMentalModel(ctx context.Context, bankID, id string, patch MentalModelPatch) (MentalModel, error) {
	current, err := s.GetMentalModel(ctx, bankID, id)
	if err != nil {
		return MentalModel{}, err
	}
	applyMentalModelPatch(&current, patch)
	return s.PutMentalModel(ctx, current)
}

// DeleteMentalModel removes a mental model.
func (s *PostgresStore) DeleteMentalModel(ctx context.Context, bankID, id string) error {
	query := `DELETE FROM ` + quoteIdent(s.schema) + `.mental_models WHERE bank_id = $1 AND id = $2`
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
	query := `UPDATE ` + quoteIdent(s.schema) + `.mental_models
	          SET content = '', structured_content = NULL, reflect_response = NULL,
	              observations = '{"observations": []}'::jsonb, history = '[]'::jsonb,
	              last_updated = now(), last_refreshed_at = now()
	          WHERE bank_id = $1 AND id = $2`
	result, err := s.db.ExecContext(ctx, query, bankID, id)
	if err != nil {
		return MentalModel{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return MentalModel{}, err
	}
	if affected == 0 {
		return MentalModel{}, ErrMentalModelNotFound
	}
	return s.GetMentalModel(ctx, bankID, id)
}

// MentalModelHistory returns the recorded refreshes of one mental model,
// newest first.
func (s *PostgresStore) MentalModelHistory(ctx context.Context, bankID, id string) ([]MentalModelHistoryEntry, error) {
	model, err := s.GetMentalModel(ctx, bankID, id)
	if err != nil {
		return nil, err
	}
	entries := make([]MentalModelHistoryEntry, 0, len(model.History))
	for _, raw := range model.History {
		entries = append(entries, historyEntryFromMap(raw))
	}
	return entries, nil
}

// RecordMentalModelHistory appends one refresh to a mental model's history.
func (s *PostgresStore) RecordMentalModelHistory(ctx context.Context, bankID, id string, entry MentalModelHistoryEntry) error {
	model, err := s.GetMentalModel(ctx, bankID, id)
	if err != nil {
		return err
	}
	if entry.RefreshedAt.IsZero() {
		entry.RefreshedAt = time.Now().UTC()
	}
	model.History = append(model.History, historyEntryToMap(entry))
	_, err = s.PutMentalModel(ctx, model)
	return err
}

// scanMentalModel reads the shared projection.
func scanMentalModel(row rowScanner) (MentalModel, error) {
	var model MentalModel
	var subtype string
	var observations, trigger, structured, reflectResult, history []byte
	var links, tags string
	var lastRefreshedAt, lastUpdated sql.NullTime
	if err := row.Scan(&model.ID, &model.BankID, &subtype, &model.Name, &model.Description,
		&model.SourceQuery, &model.Content, &model.EntityID, &observations,
		&links, &tags, &model.MaxTokens, &trigger, &structured, &reflectResult,
		&history, &model.LastRefreshedSourceQuery, &lastRefreshedAt, &lastUpdated, &model.CreatedAt); err != nil {
		return MentalModel{}, err
	}
	model.Subtype = MentalModelSubtype(subtype)
	model.Links = decodeTextArray(links)
	model.Tags = decodeTextArray(tags)
	model.LastRefreshedAt = derefTime(lastRefreshedAt)
	model.LastUpdated = derefTime(lastUpdated)
	model.Observations = decodeJSONMap(observations)
	model.Trigger = decodeJSONMap(trigger)
	model.Structured = decodeJSONMap(structured)
	model.ReflectResult = decodeJSONMap(reflectResult)
	model.History = decodeJSONList(history)
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

// jsonHistory encodes a history list as a JSON array.
func jsonHistory(history []map[string]any) ([]byte, error) {
	if history == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(history)
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

// decodeJSONList decodes a jsonb array of objects, tolerating null.
func decodeJSONList(raw []byte) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var out []map[string]any
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

// historyEntryToMap renders a history entry for storage.
func historyEntryToMap(entry MentalModelHistoryEntry) map[string]any {
	return map[string]any{
		"refreshed_at": entry.RefreshedAt.UTC().Format(time.RFC3339Nano),
		"source_query": entry.SourceQuery,
		"content":      entry.Content,
		"reason":       entry.Reason,
	}
}

// historyEntryFromMap reads a history entry back from storage.
func historyEntryFromMap(raw map[string]any) MentalModelHistoryEntry {
	entry := MentalModelHistoryEntry{}
	if v, ok := raw["refreshed_at"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			entry.RefreshedAt = parsed
		}
	}
	if v, ok := raw["source_query"].(string); ok {
		entry.SourceQuery = v
	}
	if v, ok := raw["content"].(string); ok {
		entry.Content = v
	}
	if v, ok := raw["reason"].(string); ok {
		entry.Reason = v
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
