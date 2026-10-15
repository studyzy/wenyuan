package storage

import (
	"context"
	"database/sql"
)

// semanticArmLimit bounds how many vector neighbours the semantic arm
// considers. The bound is applied in the database rather than after the fact so
// the HNSW index does the narrowing.
const semanticArmLimit = 200

// semanticHits ranks stored memories by cosine similarity to the query vector.
//
// The distance operator is cosine (`<=>`), matching the operator class of the
// stored index, so the index is usable. Rows with no embedding are skipped
// rather than scored as zero, because an unembedded row has no semantic
// evidence either way.
func (s *PostgresStore) semanticHits(ctx context.Context, q RecallQuery, types []string, limit int) ([]recallHit, error) {
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
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1
	            AND fact_type = ANY($2)
	            AND embedding IS NOT NULL
	            AND ` + recallPredicates(q) + `
	          ORDER BY embedding <=> $3::vector
	          LIMIT $4`
	vector := EncodeEmbedding(q.QueryEmbedding)
	args := append(armArgs(q), vector, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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
