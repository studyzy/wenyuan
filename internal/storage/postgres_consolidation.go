package storage

import (
	"context"
	"database/sql"
)

// ConsolidationFacts returns the newest raw facts of a bank that have not yet
// been folded into an observation. A fact is "folded" once its consolidation
// timestamp is set, which MarkConsolidated does.
func (s *PostgresStore) ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]ConsolidationFact, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT id, text, occurred_start, occurred_end, mentioned_at
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND fact_type <> $2 AND consolidated_at IS NULL
	          ORDER BY created_at DESC
	          LIMIT $3`
	rows, err := s.db.QueryContext(ctx, query, bankID, string(TypeObservation), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var facts []ConsolidationFact
	for rows.Next() {
		var fact ConsolidationFact
		var start, end, mentioned sql.NullTime
		if err := rows.Scan(&fact.ID, &fact.Text, &start, &end, &mentioned); err != nil {
			return nil, err
		}
		fact.OccurredStart = nullTimePtr(start)
		fact.OccurredEnd = nullTimePtr(end)
		fact.MentionedAt = nullTimePtr(mentioned)
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

// ConsolidationObservations returns a bank's existing observations, newest
// first, with the facts each rests on. The evidence is the observation's own
// source_memory_ids array, which is where the reference keeps it.
func (s *PostgresStore) ConsolidationObservations(ctx context.Context, bankID string, limit int) ([]ConsolidationObservation, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT u.id, u.text, u.mentioned_at,
	                 COALESCE(u.source_memory_ids, '{}'::uuid[])::text
	          FROM ` + quoteIdent(s.schema) + `.memory_units u
	          WHERE u.bank_id = $1 AND u.fact_type = $2
	          ORDER BY u.created_at DESC
	          LIMIT $3`
	rows, err := s.db.QueryContext(ctx, query, bankID, string(TypeObservation), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	observations := []ConsolidationObservation{}
	for rows.Next() {
		var observation ConsolidationObservation
		var mentioned sql.NullTime
		var sources string
		if err := rows.Scan(&observation.ID, &observation.Text, &mentioned, &sources); err != nil {
			return nil, err
		}
		observation.MentionedAt = nullTimePtr(mentioned)
		observation.SourceIDs = decodeTextArray(sources)
		observation.ProofCount = len(observation.SourceIDs)
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

// CreateObservation records a new observation resting on the given facts.
func (s *PostgresStore) CreateObservation(ctx context.Context, bankID, text string, sourceIDs []string) (string, error) {
	unit := MemoryUnit{BankID: bankID, Text: text, FactType: TypeObservation}
	stored, err := s.UpsertUnit(ctx, unit)
	if err != nil {
		return "", err
	}
	if err := s.linkObservationSources(ctx, stored.ID, sourceIDs); err != nil {
		return "", err
	}
	return stored.ID, nil
}

// UpdateObservation rewrites an observation and attaches more evidence. A
// source already linked is left alone, so re-running a pass is idempotent.
func (s *PostgresStore) UpdateObservation(ctx context.Context, bankID, observationID, text string, sourceIDs []string) error {
	query := `UPDATE ` + quoteIdent(s.schema) + `.memory_units
	          SET text = $3, search_vector = ` + searchVectorSQL + `, mentioned_at = now()
	          WHERE bank_id = $1 AND id = $2 AND fact_type = $4`
	result, err := s.db.ExecContext(ctx, query, bankID, observationID, text, string(TypeObservation))
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrMemoryNotFound
	}
	return s.linkObservationSources(ctx, observationID, sourceIDs)
}

// DeleteObservation removes one observation. Its evidence is an array on the
// row itself, so it goes with the row.
func (s *PostgresStore) DeleteObservation(ctx context.Context, bankID, observationID string) error {
	query := `DELETE FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND id = $2 AND fact_type = $3`
	_, err := s.db.ExecContext(ctx, query, bankID, observationID, string(TypeObservation))
	return err
}

// MarkConsolidated records that the given facts have been folded.
func (s *PostgresStore) MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error {
	if len(factIDs) == 0 {
		return nil
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.memory_units
	          SET consolidated_at = now()
	          WHERE bank_id = $1 AND id = ANY($2::uuid[])`
	_, err := s.db.ExecContext(ctx, query, bankID, factIDs)
	return err
}

// linkObservationSources records the facts an observation rests on, in the
// observation's own source_memory_ids array, and keeps proof_count in step.
// Both are derived from the union of the existing evidence and the new, so
// re-running a pass is idempotent.
func (s *PostgresStore) linkObservationSources(ctx context.Context, observationID string, sourceIDs []string) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.memory_units
	          SET source_memory_ids = (
	                  SELECT array_agg(DISTINCT e)
	                  FROM unnest(COALESCE(source_memory_ids, '{}'::uuid[]) || $2::text[]::uuid[]) e),
	              proof_count = (
	                  SELECT count(DISTINCT e)
	                  FROM unnest(COALESCE(source_memory_ids, '{}'::uuid[]) || $2::text[]::uuid[]) e)
	          WHERE id = $1`
	_, err := s.db.ExecContext(ctx, query, observationID, sourceIDs)
	return err
}

// ErrMemoryNotFound reports a memory unit the store does not hold.

// decodeTextArray parses the text form PostgreSQL renders for a text array,
// which is what a column is scanned as when it is cast to text rather than
// scanned through a driver-specific array codec. It delegates to the shared
// decoder so every array column is read the same way.
func decodeTextArray(encoded string) []string {
	return decodePostgresTextArray([]byte(encoded))
}
