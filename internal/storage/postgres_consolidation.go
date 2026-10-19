package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/studyzy/wenyuan/internal/timefmt"
)

// ConsolidationTuning reports the deployment's consolidation knobs, so the pass
// reads the configured batch size and model-call budget off the store rather
// than the package defaults.
func (s *PostgresStore) ConsolidationTuning() ConsolidationTuning {
	return s.tuning.Consolidation
}

// ConsolidationFacts returns the newest raw facts of a bank that have not yet
// been folded into an observation. A fact is "folded" once its consolidation
// timestamp is set, which MarkConsolidated does.
func (s *PostgresStore) ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]ConsolidationFact, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT id, text, occurred_start, occurred_end, mentioned_at,
	                 COALESCE(tags, '{}'::text[])::text, observation_scopes::text
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
		var tags string
		var scopes sql.NullString
		if err := rows.Scan(&fact.ID, &fact.Text, &start, &end, &mentioned, &tags, &scopes); err != nil {
			return nil, err
		}
		fact.OccurredStart = nullTimePtr(start)
		fact.OccurredEnd = nullTimePtr(end)
		fact.MentionedAt = nullTimePtr(mentioned)
		fact.Tags = decodeTextArray(tags)
		if scopes.Valid && scopes.String != "" {
			fact.ObservationScopes = json.RawMessage(scopes.String)
		}
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
	                 COALESCE(u.source_memory_ids, '{}'::uuid[])::text,
	                 COALESCE(u.tags, '{}'::text[])::text,
	                 COALESCE(src.texts, ARRAY[]::text[])::text
	          FROM ` + quoteIdent(s.schema) + `.memory_units u
	          LEFT JOIN LATERAL (
	              SELECT array_agg(m.text ORDER BY source.ord) AS texts
	              FROM unnest(u.source_memory_ids) WITH ORDINALITY AS source(sid, ord)
	              JOIN ` + quoteIdent(s.schema) + `.memory_units m ON m.id = source.sid
	          ) src ON true
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
		var sources, tags, sourceTexts string
		if err := rows.Scan(&observation.ID, &observation.Text, &mentioned, &sources, &tags, &sourceTexts); err != nil {
			return nil, err
		}
		observation.MentionedAt = nullTimePtr(mentioned)
		observation.SourceIDs = decodeTextArray(sources)
		observation.SourceTexts = decodeTextArray(sourceTexts)
		observation.ProofCount = len(observation.SourceIDs)
		observation.Scope = canonicalScope(decodeTextArray(tags))
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

// ObservationDuplicates embeds an observation text and returns that embedding
// together with the in-scope observations that carry a stored embedding,
// nearest first. It reports embedded=false when the deployment has no embedding
// model, or when the text produced no vector, so the caller falls back to its
// text-similarity floor rather than losing dedup.
//
// The nearest candidates are found by the vector index's cosine distance, so a
// near-identical twin that sits far down a recency-ordered pool is still the
// first thing returned.
func (s *PostgresStore) ObservationDuplicates(ctx context.Context, bankID string, scope []string, text string, limit int) ([]float32, []ConsolidationObservation, bool, error) {
	probe := embedObservation(ctx, s.observationEmbedder, text)
	if len(probe) == 0 {
		return nil, nil, false, nil
	}
	if err := ValidateEmbedding(probe); err != nil {
		return nil, nil, false, err
	}
	if limit <= 0 {
		limit = 10
	}
	scope = canonicalScope(scope)
	// An empty scope must reach the driver as an empty array, not NULL, so the
	// containment test below matches untagged rows rather than no row at all.
	scopeArg := scope
	if scopeArg == nil {
		scopeArg = []string{}
	}
	query := `SELECT u.id, u.text, u.mentioned_at,
	                 COALESCE(u.source_memory_ids, '{}'::uuid[])::text,
	                 COALESCE(u.tags, '{}'::text[])::text,
	                 u.embedding::text
	          FROM ` + quoteIdent(s.schema) + `.memory_units u
	          WHERE u.bank_id = $1 AND u.fact_type = $2
	            AND u.embedding IS NOT NULL
	            AND cardinality(COALESCE(u.tags, '{}'::text[])) = $3
	            AND COALESCE(u.tags, '{}'::text[])::text[] @> $4::text[]
	          ORDER BY u.embedding <=> $5::vector
	          LIMIT $6`
	rows, err := s.db.QueryContext(ctx, query, bankID, string(TypeObservation), len(scope), scopeArg, EncodeEmbedding(probe), limit)
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { _ = rows.Close() }()
	observations := []ConsolidationObservation{}
	for rows.Next() {
		var observation ConsolidationObservation
		var mentioned sql.NullTime
		var sources, tags, embedding string
		if err := rows.Scan(&observation.ID, &observation.Text, &mentioned, &sources, &tags, &embedding); err != nil {
			return nil, nil, false, err
		}
		observation.MentionedAt = nullTimePtr(mentioned)
		observation.SourceIDs = decodeTextArray(sources)
		observation.ProofCount = len(observation.SourceIDs)
		observation.Scope = canonicalScope(decodeTextArray(tags))
		decoded, err := DecodeEmbedding(embedding)
		if err != nil {
			return nil, nil, false, err
		}
		observation.Embedding = decoded
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	return probe, observations, true, nil
}

// CreateObservation records a new observation resting on the given facts.
func (s *PostgresStore) CreateObservation(ctx context.Context, bankID, text string, sourceIDs []string) (string, error) {
	unit := MemoryUnit{BankID: bankID, Text: text, FactType: TypeObservation}
	unit.Embedding = embedObservation(ctx, s.observationEmbedder, text)
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
//
// The pre-update state is snapshotted into observation_history first, so what
// the conclusion said before it was reinforced remains readable, and the
// history is bounded per observation so a frequently-reinforced observation does
// not grow without bound.
func (s *PostgresStore) UpdateObservation(ctx context.Context, bankID, observationID, text string, sourceIDs []string) error {
	if s.tuning.ObservationHistory {
		if err := s.appendObservationHistory(ctx, bankID, observationID, sourceIDs); err != nil {
			return err
		}
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.memory_units
	          SET text = $3, search_vector = ` + s.searchVectorExpr("$3", "''", "''") + `, mentioned_at = now()
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

// ObservationHistoryMaxEntries bounds one observation's stored history, so a
// frequently-reinforced conclusion keeps a useful tail without growing forever.
// It is the reference's default for a store built without tuning.
const ObservationHistoryMaxEntries = DefaultHistoryMaxEntries

// observationHistorySnapshot is the pre-update state of an observation. It is
// persisted as the `content` JSON blob of one observation_history row, with the
// reference's own key names so a row written by either implementation reads the
// same.
type observationHistorySnapshot struct {
	PreviousText          *string  `json:"previous_text"`
	PreviousTags          []string `json:"previous_tags"`
	PreviousOccurredStart *string  `json:"previous_occurred_start"`
	PreviousOccurredEnd   *string  `json:"previous_occurred_end"`
	PreviousMentionedAt   *string  `json:"previous_mentioned_at"`
	NewSourceMemoryIDs    []string `json:"new_source_memory_ids"`
}

// appendObservationHistory records the observation's current state as a history
// entry, then drops the entries beyond the retention bound.
//
// The snapshot carries the observation's pre-update text, tags and timestamps
// plus the source memories this change folds in, which is what the history view
// rebuilds "what sources did this conclusion rest on before this change" from.
// An observation that does not exist yet (or was removed by a concurrent pass)
// has nothing to snapshot, which is not an error.
func (s *PostgresStore) appendObservationHistory(ctx context.Context, bankID, observationID string, sourceIDs []string) error {
	current, err := s.observationSnapshot(ctx, bankID, observationID)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	current.NewSourceMemoryIDs = sourceIDs
	if current.NewSourceMemoryIDs == nil {
		current.NewSourceMemoryIDs = []string{}
	}
	snapshot, err := json.Marshal(current)
	if err != nil {
		return err
	}
	insert := `INSERT INTO ` + quoteIdent(s.schema) + `.observation_history (observation_id, bank_id, content, changed_at)
	          VALUES ($1, $2, $3::jsonb, now())`
	if _, err := s.db.ExecContext(ctx, insert, observationID, bankID, snapshot); err != nil {
		return err
	}
	prune := `DELETE FROM ` + quoteIdent(s.schema) + `.observation_history
	          WHERE observation_id = $1
	            AND id NOT IN (
	                SELECT id FROM ` + quoteIdent(s.schema) + `.observation_history
	                WHERE observation_id = $1
	                ORDER BY changed_at DESC, id DESC
	                LIMIT $2
	            )`
	_, err = s.db.ExecContext(ctx, prune, observationID, s.observationHistoryBound())
	return err
}

// observationHistoryBound is how many entries one observation's history keeps:
// the resolved tuning when it was configured, and the reference's default when
// it was not.
func (s *PostgresStore) observationHistoryBound() int {
	if s.tuning.ObservationHistoryMax > 0 {
		return s.tuning.ObservationHistoryMax
	}
	return ObservationHistoryMaxEntries
}

// observationSnapshot reads one observation's pre-update state, or nil when it
// is gone.
func (s *PostgresStore) observationSnapshot(ctx context.Context, bankID, observationID string) (*observationHistorySnapshot, error) {
	query := `SELECT COALESCE(text,''), COALESCE(tags, '{}'::text[])::text, occurred_start, occurred_end, mentioned_at
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND id = $2 AND fact_type = $3`
	var (
		text, tagsRaw                           string
		occurredStart, occurredEnd, mentionedAt *time.Time
	)
	err := s.db.QueryRowContext(ctx, query, bankID, observationID, string(TypeObservation)).
		Scan(&text, &tagsRaw, &occurredStart, &occurredEnd, &mentionedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tags := decodePostgresTextArray([]byte(tagsRaw))
	if tags == nil {
		tags = []string{}
	}
	return &observationHistorySnapshot{
		PreviousText:          &text,
		PreviousTags:          tags,
		PreviousOccurredStart: timefmt.FormatTimestampPtr(occurredStart),
		PreviousOccurredEnd:   timefmt.FormatTimestampPtr(occurredEnd),
		PreviousMentionedAt:   timefmt.FormatTimestampPtr(mentionedAt),
	}, nil
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

// MarkConsolidationFailed records that a pass gave up on the given facts. The
// candidates read excludes them (consolidation_failed_at IS NOT NULL), so they
// stay out of later passes until RecoverConsolidations requeues them.
func (s *PostgresStore) MarkConsolidationFailed(ctx context.Context, bankID string, factIDs []string) error {
	if len(factIDs) == 0 {
		return nil
	}
	query := `UPDATE ` + quoteIdent(s.schema) + `.memory_units
	          SET consolidation_failed_at = now()
	          WHERE bank_id = $1 AND id = ANY($2::uuid[])`
	_, err := s.db.ExecContext(ctx, query, bankID, factIDs)
	return err
}

// DeleteStaleObservations removes the observations that rest on any of the
// given facts, and requeues the source facts they shared with them. It mirrors
// the reference's delete_stale_observations: an observation's text was
// synthesised from its evidence, so once even one source is about to disappear
// the observation is stale and must go rather than keep answering.
//
// It runs in a transaction of its own, which is what a standalone caller wants.
// A caller that must sweep inside a wider delete transaction (so the sweep and
// the cascade hold one set of locks) calls deleteStaleObservationsTx directly.
func (s *PostgresStore) DeleteStaleObservations(ctx context.Context, bankID string, factIDs []string) (int, error) {
	if len(factIDs) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	deleted, err := s.deleteStaleObservationsTx(ctx, tx, bankID, factIDs)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// deleteStaleObservationsTx is the body of the sweep, run on the caller's
// transaction so it can share the locks a delete cascade holds.
func (s *PostgresStore) deleteStaleObservationsTx(ctx context.Context, tx Tx, bankID string, factIDs []string) (int, error) {
	if len(factIDs) == 0 {
		return 0, nil
	}
	qualified := quoteIdent(s.schema)
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(source_memory_ids, '{}'::uuid[])::text
		 FROM `+qualified+`.memory_units
		 WHERE bank_id = $1 AND fact_type = $2 AND source_memory_ids && $3::uuid[]`,
		bankID, string(TypeObservation), factIDs)
	if err != nil {
		return 0, err
	}
	doomed := make([]string, 0)
	deleted := make(map[string]bool, len(factIDs))
	for _, id := range factIDs {
		deleted[id] = true
	}
	remaining := make([]string, 0)
	seen := map[string]bool{}
	for rows.Next() {
		var observationID, sources string
		if err := rows.Scan(&observationID, &sources); err != nil {
			_ = rows.Close()
			return 0, err
		}
		doomed = append(doomed, observationID)
		for _, source := range decodePostgresTextArray([]byte(sources)) {
			if deleted[source] || seen[source] {
				continue
			}
			seen[source] = true
			remaining = append(remaining, source)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(doomed) == 0 {
		return 0, nil
	}
	// Lock every row this sweep touches — the outgoing facts, the observations
	// and their surviving co-sources, which may belong to another document — in
	// one id order before writing any of them. Two sweeps over a shared
	// observation otherwise each hold a row the other writes next and deadlock;
	// taking the locks in a fixed order means the second sweep simply waits.
	// FOR NO KEY UPDATE still excludes a concurrent sweep or consolidation's
	// shared lock, but lets foreign-key checks on these rows finish.
	lockIDs := make([]string, 0, len(factIDs)+len(doomed)+len(remaining))
	lockIDs = append(lockIDs, factIDs...)
	lockIDs = append(lockIDs, doomed...)
	lockIDs = append(lockIDs, remaining...)
	if _, err := tx.ExecContext(ctx,
		`SELECT id FROM `+qualified+`.memory_units
		 WHERE bank_id = $1 AND id = ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE`,
		bankID, lockIDs); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, doomed); err != nil {
		return 0, err
	}
	// The history is keyed by observation_id and does not cascade from
	// memory_units, so drop the deleted observations' snapshots explicitly
	// rather than leaving them to accumulate unreachable.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.observation_history WHERE bank_id = $1 AND observation_id = ANY($2::uuid[])`,
		bankID, doomed); err != nil {
		return 0, err
	}
	if len(remaining) > 0 {
		// The surviving co-sources re-consolidate under a fresh observation;
		// nothing about them changed, so only the scheduler marker is cleared.
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units SET consolidated_at = NULL
			 WHERE bank_id = $1 AND id = ANY($2::uuid[]) AND fact_type IN ('experience', 'world')`,
			bankID, remaining); err != nil {
			return 0, err
		}
	}
	return len(doomed), nil
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
