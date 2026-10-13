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
	query := `SELECT id, text, type, occurred_start, occurred_end, mentioned_at, chunk_id, document_id,
	                 embedding <=> $2::vector AS distance
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1
	            AND type = ANY($3)
	            AND embedding IS NOT NULL
	          ORDER BY embedding <=> $2::vector
	          LIMIT $4`
	vector := EncodeEmbedding(q.QueryEmbedding)
	rows, err := s.db.QueryContext(ctx, query, q.BankID, vector, types, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	hits := make([]recallHit, 0)
	for rows.Next() {
		var m RecallMatch
		var occurredStart, occurredEnd, mentionedAt sql.NullTime
		var chunkID, documentID sql.NullString
		var distance float64
		if err := rows.Scan(&m.ID, &m.Text, &m.Type, &occurredStart, &occurredEnd,
			&mentionedAt, &chunkID, &documentID, &distance); err != nil {
			return nil, err
		}
		m.OccurredStart = nullTimePtr(occurredStart)
		m.OccurredEnd = nullTimePtr(occurredEnd)
		m.MentionedAt = nullTimePtr(mentionedAt)
		m.ChunkID = chunkID.String
		m.DocumentID = documentID.String
		hits = append(hits, recallHit{match: m, score: CosineSimilarity(distance)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

// embeddingProviderAvailable reports whether a query vector was supplied, which
// is what selects the real semantic arm over the stand-in.
func embeddingProviderAvailable(q RecallQuery) bool {
	return len(q.QueryEmbedding) > 0
}

// vectorRecall is the semantic-first recall path. It is used when the caller
// supplied an embedded query, so ranking is by cosine similarity rather than by
// textual containment.
func (s *PostgresStore) vectorRecall(ctx context.Context, q RecallQuery, types []string, limit int) (RecallResult, error) {
	hits, err := s.semanticHits(ctx, q, types, limit)
	if err != nil {
		return RecallResult{}, err
	}
	if floor, ok := q.MinScores[ArmSemantic]; ok {
		filtered := hits[:0]
		for _, hit := range hits {
			if hit.score >= floor {
				filtered = append(filtered, hit)
			}
		}
		hits = filtered
	}

	matches := make([]RecallMatch, 0, len(hits))
	for rank, hit := range hits {
		match := hit.match
		match.Scores = map[string]float64{ArmSemantic: hit.score}
		match.ArmRanks = map[string]int{ArmSemantic: rank + 1}
		match.Final = hit.score
		matches = append(matches, match)
	}
	return RecallResult{Matches: matches}, nil
}
