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

// ConsolidationFacts returns the oldest raw facts of a bank that have not yet
// been folded into an observation. Consolidation works oldest-first: the
// reference sorts its candidates by created_at ascending
// (consolidator.py:1459-1461), so a fact's turn comes in the order it was
// written. A fact is "folded" once its consolidation timestamp is set, which
// MarkConsolidated does.
func (s *PostgresStore) ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]ConsolidationFact, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []ConsolidationFact
		return zero0, schemaErr
	}
	if limit <= 0 {
		limit = 200
	}
	query := `SELECT id, text, created_at, occurred_start, occurred_end, mentioned_at,
	                 COALESCE(tags, '{}'::text[])::text, observation_scopes::text
	          FROM ` + quoteIdent(schema) + `.memory_units
	          WHERE bank_id = $1 AND fact_type <> $2 AND consolidated_at IS NULL
	                AND consolidation_failed_at IS NULL
	          ORDER BY created_at ASC
	          LIMIT $3`
	rows, err := s.db.QueryContext(ctx, query, bankID, string(TypeObservation), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var facts []ConsolidationFact
	for rows.Next() {
		var fact ConsolidationFact
		var created, start, end, mentioned sql.NullTime
		var tags string
		var scopes sql.NullString
		if err := rows.Scan(&fact.ID, &fact.Text, &created, &start, &end, &mentioned, &tags, &scopes); err != nil {
			return nil, err
		}
		fact.CreatedAt = nullTimePtr(created)
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []ConsolidationObservation
		return zero0, schemaErr
	}
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT u.id, u.text, u.mentioned_at,
	                 COALESCE(u.source_memory_ids, '{}'::uuid[])::text,
	                 COALESCE(u.tags, '{}'::text[])::text,
	                 COALESCE(src.texts, ARRAY[]::text[])::text
	          FROM ` + quoteIdent(schema) + `.memory_units u
	          LEFT JOIN LATERAL (
	              SELECT array_agg(m.text ORDER BY source.ord) AS texts
	              FROM unnest(u.source_memory_ids) WITH ORDINALITY AS source(sid, ord)
	              JOIN ` + quoteIdent(schema) + `.memory_units m ON m.id = source.sid
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []float32
		var zero1 []ConsolidationObservation
		var zero2 bool
		return zero0, zero1, zero2, schemaErr
	}
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
	          FROM ` + quoteIdent(schema) + `.memory_units u
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
	// An observation has no context of its own: the reference's insert never
	// names the column (engine/memories/pg/consolidation.py:268-320), so the row
	// carries NULL and a recall result reports `context: null` rather than "".
	unit := MemoryUnit{BankID: bankID, Text: text, FactType: TypeObservation, ContextAbsent: true}
	bounds, err := s.sourceObservationBounds(ctx, bankID, sourceIDs)
	if err != nil {
		return "", err
	}
	bounds.ApplyTo(&unit)
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if s.tuning.ObservationHistory {
		if err := s.appendObservationHistory(ctx, bankID, observationID, sourceIDs); err != nil {
			return err
		}
	}
	// The temporal columns widen with the new sources rather than being stamped
	// with the write clock. The reference merges the sources' bounds into the
	// observation's (`_TemporalBounds.merged_with`, consolidator.py:257-262),
	// which keeps an observation that rests on an old fact an old observation.
	// The merge runs on the Go side over both sets of columns so a year-1/9999
	// placeholder reads as unknown on either side — the reference's
	// _as_dt→finite_utc — and a merged bound is NULL when only placeholders
	// remain; the SQL LEAST/GREATEST it replaces would keep a placeholder on
	// the observation's own columns forever.
	sources, err := s.sourceObservationBounds(ctx, bankID, sourceIDs)
	if err != nil {
		return err
	}
	current, err := s.sourceObservationBounds(ctx, bankID, []string{observationID})
	if err != nil {
		return err
	}
	bounds := current.MergedWith(sources)
	query := `UPDATE ` + quoteIdent(schema) + `.memory_units
	          SET text = $3, search_vector = ` + s.searchVectorExpr("$3", "''", "''") + `,
	              event_date = $5,
	              occurred_start = $6,
	              occurred_end = $7,
	              mentioned_at = $8
	          WHERE bank_id = $1 AND id = $2 AND fact_type = $4`
	result, err := s.db.ExecContext(ctx, query, bankID, observationID, text, string(TypeObservation),
		bounds.EventDate, bounds.OccurredStart, bounds.OccurredEnd, bounds.MentionedAt)
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
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
	insert := `INSERT INTO ` + quoteIdent(schema) + `.observation_history (observation_id, bank_id, content, changed_at)
	          VALUES ($1, $2, $3::jsonb, now())`
	if _, err := s.db.ExecContext(ctx, insert, observationID, bankID, snapshot); err != nil {
		return err
	}
	prune := `DELETE FROM ` + quoteIdent(schema) + `.observation_history
	          WHERE observation_id = $1
	            AND id NOT IN (
	                SELECT id FROM ` + quoteIdent(schema) + `.observation_history
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 *observationHistorySnapshot
		return zero0, schemaErr
	}
	query := `SELECT COALESCE(text,''), COALESCE(tags, '{}'::text[])::text, occurred_start, occurred_end, mentioned_at
	          FROM ` + quoteIdent(schema) + `.memory_units
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `DELETE FROM ` + quoteIdent(schema) + `.memory_units
	          WHERE bank_id = $1 AND id = $2 AND fact_type = $3`
	_, err := s.db.ExecContext(ctx, query, bankID, observationID, string(TypeObservation))
	return err
}

// MarkConsolidated records that the given facts have been folded.
func (s *PostgresStore) MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if len(factIDs) == 0 {
		return nil
	}
	query := `UPDATE ` + quoteIdent(schema) + `.memory_units
	          SET consolidated_at = now()
	          WHERE bank_id = $1 AND id = ANY($2::uuid[])`
	_, err := s.db.ExecContext(ctx, query, bankID, factIDs)
	return err
}

// MarkConsolidationFailed records that a pass gave up on the given facts. The
// candidates read excludes them (consolidation_failed_at IS NOT NULL), so they
// stay out of later passes until RecoverConsolidations requeues them.
func (s *PostgresStore) MarkConsolidationFailed(ctx context.Context, bankID string, factIDs []string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if len(factIDs) == 0 {
		return nil
	}
	query := `UPDATE ` + quoteIdent(schema) + `.memory_units
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
	removed, err := s.deleteStaleObservationsReportingTx(ctx, tx, bankID, factIDs)
	noteSweptObservations(ctx, removed, factIDs)
	return len(removed), err
}

// deleteStaleObservationsReportingTx is the sweep, reporting each removed
// observation with the memories it was consolidated from — what the document
// change history records for a retag.
func (s *PostgresStore) deleteStaleObservationsReportingTx(
	ctx context.Context, tx Tx, bankID string, factIDs []string,
) ([]RemovedObservation, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	if len(factIDs) == 0 {
		return nil, nil
	}
	qualified := quoteIdent(schema)
	rows, err := tx.QueryContext(ctx,
		`SELECT id, COALESCE(source_memory_ids, '{}'::uuid[])::text
		 FROM `+qualified+`.memory_units
		 WHERE bank_id = $1 AND fact_type = $2 AND source_memory_ids && $3::uuid[]`,
		bankID, string(TypeObservation), factIDs)
	if err != nil {
		return nil, err
	}
	doomed := make([]string, 0)
	deleted := make(map[string]bool, len(factIDs))
	for _, id := range factIDs {
		deleted[id] = true
	}
	remaining := make([]string, 0)
	seen := map[string]bool{}
	removed := make([]RemovedObservation, 0)
	for rows.Next() {
		var observationID, sources string
		if err := rows.Scan(&observationID, &sources); err != nil {
			_ = rows.Close()
			return nil, err
		}
		doomed = append(doomed, observationID)
		observations := decodePostgresTextArray([]byte(sources))
		removed = append(removed, RemovedObservation{ObservationID: observationID, SourceFactIDs: observations})
		for _, source := range observations {
			if deleted[source] || seen[source] {
				continue
			}
			seen[source] = true
			remaining = append(remaining, source)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(doomed) == 0 {
		return nil, nil
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
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, doomed); err != nil {
		return nil, err
	}
	// The history is keyed by observation_id and does not cascade from
	// memory_units, so drop the deleted observations' snapshots explicitly
	// rather than leaving them to accumulate unreachable — and rather than
	// letting an observation that later reuses the id inherit a history it never
	// had. The reference deletes them in the same order, right after the units
	// (engine/memories/pg/writes.py:317-322).
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.observation_history WHERE bank_id = $1 AND observation_id = ANY($2::uuid[])`,
		bankID, doomed); err != nil {
		return nil, err
	}
	if len(remaining) > 0 {
		// The surviving co-sources re-consolidate under a fresh observation;
		// nothing about them changed, so only the scheduler marker is cleared.
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units SET consolidated_at = NULL
			 WHERE bank_id = $1 AND id = ANY($2::uuid[]) AND fact_type IN ('experience', 'world')`,
			bankID, remaining); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

// deleteObservationsOnTx removes the observations resting on any of factIDs and
// clears each fact's consolidation marker, without requeueing the observations'
// surviving co-sources: a scope change re-consolidates only the memories that
// moved, and a co-source of another document may have stayed where it was
// (#5593). It answers with what it removed, for the document's change history.
func (s *PostgresStore) deleteObservationsOnTx(
	ctx context.Context, tx Tx, bankID string, factIDs []string,
) ([]RemovedObservation, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	if len(factIDs) == 0 {
		return nil, nil
	}
	qualified := quoteIdent(schema)
	rows, err := tx.QueryContext(ctx,
		`SELECT id::text, COALESCE(source_memory_ids, '{}'::uuid[])::text
		   FROM `+qualified+`.memory_units
		  WHERE bank_id = $1 AND fact_type = $2 AND source_memory_ids && $3::uuid[]
		  ORDER BY id`,
		bankID, string(TypeObservation), factIDs)
	if err != nil {
		return nil, err
	}
	removed := make([]RemovedObservation, 0)
	doomed := make([]string, 0)
	for rows.Next() {
		var observationID, sources string
		if err := rows.Scan(&observationID, &sources); err != nil {
			_ = rows.Close()
			return nil, err
		}
		doomed = append(doomed, observationID)
		removed = append(removed, RemovedObservation{
			ObservationID: observationID, SourceFactIDs: decodePostgresTextArray([]byte(sources)),
		})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(doomed) == 0 {
		return removed, nil
	}
	// The rows are locked in one id order before any is written, so a concurrent
	// sweep cannot hold the next row this one writes and deadlock with it.
	if _, err := tx.ExecContext(ctx,
		`SELECT id FROM `+qualified+`.memory_units
		  WHERE bank_id = $1 AND id = ANY($2::uuid[]) ORDER BY id FOR NO KEY UPDATE`,
		bankID, doomed); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_units WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
		bankID, doomed); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+qualified+`.observation_history WHERE bank_id = $1 AND observation_id = ANY($2::uuid[])`,
		bankID, doomed); err != nil {
		return nil, err
	}
	return removed, nil
}

// linkObservationSources records the facts an observation rests on, in the
// observation's own source_memory_ids array, and keeps proof_count in step.
// Both are derived from the union of the existing evidence and the new, so
// re-running a pass is idempotent.
func (s *PostgresStore) linkObservationSources(ctx context.Context, observationID string, sourceIDs []string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if len(sourceIDs) == 0 {
		return nil
	}
	query := `UPDATE ` + quoteIdent(schema) + `.memory_units
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

// sourceObservationBounds aggregates the temporal bounds a fact set hands to the
// observation it is folded into, reading the facts' own columns. The reference's
// rule is `_aggregate_source_fields` (consolidator.py:838-860): earliest
// event_date and occurred_start, latest occurred_end and mentioned_at.
func (s *PostgresStore) sourceObservationBounds(ctx context.Context, bankID string, sourceIDs []string) (ObservationBounds, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 ObservationBounds
		return zero0, schemaErr
	}
	if len(sourceIDs) == 0 {
		return ObservationBounds{}, nil
	}
	query := `SELECT event_date, occurred_start, occurred_end, mentioned_at
	          FROM ` + quoteIdent(schema) + `.memory_units
	          WHERE bank_id = $1 AND id = ANY($2::uuid[])`
	rows, err := s.db.QueryContext(ctx, query, bankID, sourceIDs)
	if err != nil {
		return ObservationBounds{}, err
	}
	defer func() { _ = rows.Close() }()
	sources := make([]MemoryUnit, 0, len(sourceIDs))
	for rows.Next() {
		var unit MemoryUnit
		var eventDate, occurredStart, occurredEnd, mentionedAt sql.NullTime
		if err := rows.Scan(&eventDate, &occurredStart, &occurredEnd, &mentionedAt); err != nil {
			return ObservationBounds{}, err
		}
		unit.EventDate = nullTimePtr(eventDate)
		unit.OccurredStart = nullTimePtr(occurredStart)
		unit.OccurredEnd = nullTimePtr(occurredEnd)
		unit.MentionedAt = nullTimePtr(mentionedAt)
		sources = append(sources, unit)
	}
	if err := rows.Err(); err != nil {
		return ObservationBounds{}, err
	}
	return AggregatedObservationBounds(sources), nil
}

// MergeObservationEvidence moves a merged-away observation's evidence onto the
// observation that absorbed it, or requeues that evidence when there is no
// survivor to receive it. It is the reference's _fold_deleted_evidence
// (consolidator.py:3335-3380): only a delete the model marks as a merge
// (merged_into) moves its evidence, and a merge whose target the model could
// not name — or whose survivor is already gone — requeues its live sources
// instead (mark_consolidated(when=None)), so the next pass attaches them
// wherever the knowledge now belongs. The caller deletes the merged
// observation afterwards; the row must still exist when this runs.
func (s *PostgresStore) MergeObservationEvidence(ctx context.Context, bankID, survivorID, mergedID string) error {
	if mergedID == "" {
		return nil
	}
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	// The merged observation's evidence and dates, read before the caller
	// deletes it: the fold widens the survivor's dates with them.
	var (
		sourcesRaw                       string
		eventDate, start, end, mentioned sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(source_memory_ids, '{}'::uuid[])::text,
	          event_date, occurred_start, occurred_end, mentioned_at
	          FROM `+quoteIdent(schema)+`.memory_units
	          WHERE bank_id = $1 AND id = $2`, bankID, mergedID).
		Scan(&sourcesRaw, &eventDate, &start, &end, &mentioned)
	if errors.Is(err, sql.ErrNoRows) {
		// The row is already gone: there is no evidence to move.
		return nil
	}
	if err != nil {
		return err
	}
	mergedBounds := ObservationBounds{
		EventDate: nullTimePtr(eventDate), OccurredStart: nullTimePtr(start),
		OccurredEnd: nullTimePtr(end), MentionedAt: nullTimePtr(mentioned),
	}
	// Only the sources that still exist move; a dead link contributes nothing,
	// as the reference's liveness filter does (_filter_live_source_memories).
	live, err := s.liveUnitIDs(ctx, bankID, decodeTextArray(sourcesRaw))
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return nil
	}
	if survivorID != "" {
		var factType string
		survivorErr := s.db.QueryRowContext(ctx, `SELECT fact_type::text
		          FROM `+quoteIdent(schema)+`.memory_units
		          WHERE bank_id = $1 AND id = $2`, bankID, survivorID).Scan(&factType)
		if survivorErr != nil && !errors.Is(survivorErr, sql.ErrNoRows) {
			return survivorErr
		}
		if survivorErr == nil && factType == string(TypeObservation) {
			current, err := s.sourceObservationBounds(ctx, bankID, []string{survivorID})
			if err != nil {
				return err
			}
			// The survivor keeps its text; its dates widen with the merged
			// observation's and its evidence grows by the merged sources
			// (fold_sources_into_observation keeps the embedding too).
			bounds := current.MergedWith(mergedBounds)
			updated, err := s.db.ExecContext(ctx, `UPDATE `+quoteIdent(schema)+`.memory_units
			          SET event_date = $3, occurred_start = $4, occurred_end = $5, mentioned_at = $6
			          WHERE bank_id = $1 AND id = $2 AND fact_type = $7`,
				bankID, survivorID, bounds.EventDate, bounds.OccurredStart, bounds.OccurredEnd, bounds.MentionedAt,
				string(TypeObservation))
			if err != nil {
				return err
			}
			if affected, err := updated.RowsAffected(); err == nil && affected > 0 {
				return s.linkObservationSources(ctx, survivorID, live)
			}
			// The survivor vanished between the read and the write: the
			// evidence is requeued below, the way a fold that found no row is.
		}
	}
	// No usable survivor: the evidence is requeued, keeping the
	// experience/world guard the reference's clear carries — observations are
	// never themselves consolidated, so nothing about them is reset.
	_, err = s.db.ExecContext(ctx, `UPDATE `+quoteIdent(schema)+`.memory_units
	          SET consolidated_at = NULL
	          WHERE bank_id = $1 AND id = ANY($2::uuid[]) AND fact_type IN ('experience', 'world')`,
		bankID, live)
	return err
}

// liveUnitIDs returns the ids that resolve to a row of the bank, the liveness
// filter a fold applies to the evidence it moves.
func (s *PostgresStore) liveUnitIDs(ctx context.Context, bankID string, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return nil, schemaErr
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id::text
	          FROM `+quoteIdent(schema)+`.memory_units
	          WHERE bank_id = $1 AND id = ANY($2::uuid[])`, bankID, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var live []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live = append(live, id)
	}
	return live, rows.Err()
}

// EnqueueConsolidationSuccessor records the next round of a round-capped
// consolidation chain, carrying callerRequested in the task payload the way the
// reference's submit_async_consolidation does (memory_engine.py:23716-23717).
// It deduplicates against a pending full-bank pass only — the pass now running
// is this one, and folding the successor into it would swallow the chain
// (engine/memory_engine.py:22207-22211).
func (s *PostgresStore) EnqueueConsolidationSuccessor(ctx context.Context, bankID string, callerRequested bool) error {
	op := Operation{
		OperationID: newID(), BankID: bankID, OperationType: OperationTypeConsolidate,
		Status: StatusPending, SerializationKey: ConsolidationSerializationKey(bankID),
	}
	if callerRequested {
		op.Payload = map[string]any{"caller_requested": true}
	}
	_, err := s.enqueueDedupeByBank(ctx, op, []string{"pending"}, true)
	return err
}
