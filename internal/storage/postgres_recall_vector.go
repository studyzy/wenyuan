package storage

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// semanticArmLimit bounds how many vector neighbours the semantic arm
// considers. The bound is applied in the database rather than after the fact so
// the HNSW index does the narrowing.
const semanticArmLimit = 200

// annRows runs the semantic arm with the deployment's ANN tuning applied
// transaction-locally, the way the reference tunes its high-recall profile:
// `hnsw.iterative_scan` lets a scan resume in rounds until the arm's LIMIT is
// met rather than stopping when the first list drains, and
// `hnsw.max_scan_tuples` bounds how far it may go.
//
// A server that rejects a setting (pgvector older than 0.8 has no
// `hnsw.iterative_scan`) is remembered as rejecting it, and that setting is
// skipped from then on: applying it inside the arm's transaction would abort
// the query rather than merely fail to take effect. A deployment on another
// vector backend gets no ANN tuning at all, because the settings are pgvector's
// own.
// The returned function closes the rows and, when the query ran in a
// transaction, ends it: the caller reads the rows first and calls it after.
func (s *PostgresStore) annRows(ctx context.Context, query string, args []any) (*sql.Rows, func(), error) {
	settings := s.annSettings()
	if len(settings) == 0 {
		rows, err := s.reader().QueryContext(ctx, query, args...)
		if err != nil {
			return nil, nil, err
		}
		return rows, func() { _ = rows.Close() }, nil
	}
	tx, err := s.reader().BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	for _, setting := range settings {
		if s.annSettingRejected(setting.name) {
			continue
		}
		if _, err := tx.ExecContext(ctx, "SET LOCAL "+setting.name+" = "+sqlTextLiteral(setting.value)); err != nil {
			// A server that predates the setting is not asked again: applying
			// it inside this transaction would abort the query rather than
			// merely fail to take effect.
			s.rememberAnnSettingRejected(setting.name)
		}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, err
	}
	return rows, func() {
		_ = rows.Close()
		_ = tx.Commit()
	}, nil
}

// annSetting is one pgvector setting the semantic arm applies.
type annSetting struct {
	name  string
	value string
}

// annSettings is the tuning the semantic arm applies, or none when the
// deployment runs another vector backend.
func (s *PostgresStore) annSettings() []annSetting {
	// An unset extension is the pgvector default, the same reading the index
	// access method uses: only a deployment that named another backend gets no
	// pgvector tuning.
	switch strings.ToLower(strings.TrimSpace(s.vectorExtension)) {
	case "", "pgvector":
	default:
		return nil
	}
	settings := make([]annSetting, 0, 3)
	// The recall profile the reference runs: a deeper search than the index's
	// default, resumed in rounds.
	settings = append(settings, annSetting{name: "hnsw.ef_search", value: "200"})
	if s.tuning.ANNIterativeScan {
		settings = append(settings, annSetting{name: "hnsw.iterative_scan", value: "strict_order"})
	} else {
		settings = append(settings, annSetting{name: "hnsw.iterative_scan", value: "off"})
	}
	if s.tuning.ANNMaxScanTuples > 0 {
		settings = append(settings, annSetting{name: "hnsw.max_scan_tuples", value: strconv.Itoa(s.tuning.ANNMaxScanTuples)})
	}
	return settings
}

// annSettingRejected reports whether the server has already refused a setting.
func (s *PostgresStore) annSettingRejected(name string) bool {
	s.annRejectedMu.Lock()
	defer s.annRejectedMu.Unlock()
	return s.annRejected[name]
}

// rememberAnnSettingRejected records a refusal so later queries skip it.
func (s *PostgresStore) rememberAnnSettingRejected(name string) {
	s.annRejectedMu.Lock()
	defer s.annRejectedMu.Unlock()
	if s.annRejected == nil {
		s.annRejected = map[string]bool{}
	}
	s.annRejected[name] = true
}

// semanticHits ranks stored memories by cosine similarity to the query vector.
//
// The distance operator is cosine (`<=>`), matching the operator class of the
// stored index, so the index is usable. Rows with no embedding are skipped
// rather than scored as zero, because an unembedded row has no semantic
// evidence either way.
func (s *PostgresStore) semanticHits(ctx context.Context, q RecallQuery, types []string, limit int) ([]recallHit, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []recallHit
		return zero0, schemaErr
	}
	if len(q.QueryEmbedding) == 0 {
		return nil, nil
	}
	if err := ValidateEmbedding(q.QueryEmbedding); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > semanticArmLimit {
		limit = semanticArmLimit
	}
	query := `SELECT id::text, text, fact_type, occurred_start, occurred_end, mentioned_at, chunk_id, document_id, context, metadata, tags,
	                 COALESCE(source_memory_ids, '{}'::uuid[])::text,
	                 embedding <=> $3::vector AS distance
	          FROM ` + quoteIdent(schema) + `.memory_units
	          WHERE bank_id = $1
	            AND fact_type = ANY($2)
	            AND embedding IS NOT NULL
	            AND ` + recallPredicates(q) + `
	          ORDER BY embedding <=> $3::vector
	          LIMIT $4`
	vector := EncodeEmbedding(q.QueryEmbedding)
	args := append(armArgs(q), vector, limit)
	rows, done, err := s.annRows(ctx, query, args)
	if err != nil {
		return nil, err
	}
	defer done()

	hits := make([]recallHit, 0)
	for rows.Next() {
		var m RecallMatch
		var occurredStart, occurredEnd, mentionedAt sql.NullTime
		var chunkID, documentID, context sql.NullString
		var metadataRaw, tagsRaw, sourceRaw []byte
		var distance float64
		if err := rows.Scan(&m.ID, &m.Text, &m.Type, &occurredStart, &occurredEnd,
			&mentionedAt, &chunkID, &documentID, &context, &metadataRaw, &tagsRaw, &sourceRaw, &distance); err != nil {
			return nil, err
		}
		m.OccurredStart = utcTimePtr(nullTimePtr(occurredStart))
		m.OccurredEnd = utcTimePtr(nullTimePtr(occurredEnd))
		m.MentionedAt = utcTimePtr(nullTimePtr(mentionedAt))
		m.ChunkID = chunkID.String
		m.DocumentID = documentID.String
		m.Context = context.String
		m.Metadata = decodeJSONMap(metadataRaw)
		m.Tags = decodePostgresTextArray(tagsRaw)
		m.SourceMemoryIDs = decodePostgresTextArray(sourceRaw)
		hits = append(hits, recallHit{match: m, score: CosineSimilarity(distance)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}
