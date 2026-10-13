package storage

import (
	"context"
	"database/sql"
	"strings"
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
	          WHERE bank_id = $1 AND type <> $2 AND consolidated_at IS NULL
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
// first, with the facts each rests on.
func (s *PostgresStore) ConsolidationObservations(ctx context.Context, bankID string, limit int) ([]ConsolidationObservation, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT u.id, u.text, u.mentioned_at,
	                 COALESCE(array_agg(os.source_id) FILTER (WHERE os.source_id IS NOT NULL), '{}'::text[])
	          FROM ` + quoteIdent(s.schema) + `.memory_units u
	          LEFT JOIN ` + quoteIdent(s.schema) + `.observation_sources os ON os.observation_id = u.id
	          WHERE u.bank_id = $1 AND u.type = $2
	          GROUP BY u.id, u.text, u.mentioned_at, u.created_at
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
	unit := MemoryUnit{BankID: bankID, Text: text, Type: TypeObservation}
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
	          SET text = $3, mentioned_at = now()
	          WHERE bank_id = $1 AND id = $2 AND type = $4`
	result, err := s.db.ExecContext(ctx, query, bankID, observationID, text, string(TypeObservation))
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrMemoryNotFound
	}
	return s.linkObservationSources(ctx, observationID, sourceIDs)
}

// DeleteObservation removes one observation and its evidence links.
func (s *PostgresStore) DeleteObservation(ctx context.Context, bankID, observationID string) error {
	query := `DELETE FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND id = $2 AND type = $3`
	if _, err := s.db.ExecContext(ctx, query, bankID, observationID, string(TypeObservation)); err != nil {
		return err
	}
	links := `DELETE FROM ` + quoteIdent(s.schema) + `.observation_sources WHERE observation_id = $1`
	_, err := s.db.ExecContext(ctx, links, observationID)
	return err
}

// MarkConsolidated records that the given facts have been folded.
func (s *PostgresStore) MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error {
	if len(factIDs) == 0 {
		return nil
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.memory_units
	          SET consolidated_at = now()
	          WHERE bank_id = $1 AND id = ANY($2::text[])`
	_, err := s.db.ExecContext(ctx, query, bankID, factIDs)
	return err
}

// linkObservationSources records the facts an observation rests on. The write
// is idempotent because the edge is a primary key.
func (s *PostgresStore) linkObservationSources(ctx context.Context, observationID string, sourceIDs []string) error {
	if len(sourceIDs) == 0 {
		return nil
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.observation_sources (observation_id, source_id)
	          SELECT $1, unnest($2::text[])
	          ON CONFLICT DO NOTHING`
	_, err := s.db.ExecContext(ctx, query, observationID, sourceIDs)
	return err
}

// ErrMemoryNotFound reports a memory unit the store does not hold.

// decodeTextArray parses the text form PostgreSQL renders for a text array,
// which is what a grouped aggregate is scanned as without a driver-specific
// array decoder.
func decodeTextArray(encoded string) []string {
	trimmed := strings.TrimSpace(encoded)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return []string{}
	}
	body := trimmed[1 : len(trimmed)-1]
	if strings.TrimSpace(body) == "" {
		return []string{}
	}
	var values []string
	var current strings.Builder
	inQuotes := false
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
		case c == '\\' && i+1 < len(body):
			i++
			current.WriteByte(body[i])
		case c == ',' && !inQuotes:
			values = append(values, current.String())
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	values = append(values, current.String())
	return values
}
