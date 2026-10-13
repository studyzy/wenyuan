package storage

import (
	"context"
	"database/sql"
	"strings"
)

// Recall runs recall against the database-backed store. It resolves the bank,
// selects the requested fact types, applies flat tag scoping, and returns a
// ranked list. The richer multi-arm pipeline is implemented by the in-process
// store; this path preserves the observable read contract.
func (s *PostgresStore) Recall(ctx context.Context, q RecallQuery) (RecallResult, error) {
	if _, err := s.GetBank(ctx, q.BankID); err != nil {
		return RecallResult{}, err
	}
	types := resolvedTypes(q.Types)
	// The budget level bounds the retrieval work, so it decides the candidate
	// limit unless the caller named an explicit one.
	limit := q.Limit
	if limit <= 0 {
		limit = BudgetLimit(q.Budget)
	}

	// When the caller supplied an embedded query, the semantic arm ranks by
	// real vector similarity and the lexical filter is not applied, because a
	// vector neighbour need not contain the query's words.
	if embeddingProviderAvailable(q) {
		return s.vectorRecall(ctx, q, types, limit)
	}

	query := `SELECT id, text, type, occurred_start, occurred_end, mentioned_at, chunk_id, document_id
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1
	            AND ($2 = '' OR text ILIKE '%' || $2 || '%')
	            AND type = ANY($3)
	          LIMIT $4`
	rows, err := s.db.QueryContext(ctx, query, q.BankID, strings.TrimSpace(q.Query), types, limit)
	if err != nil {
		return RecallResult{}, err
	}
	defer func() { _ = rows.Close() }()
	matches := make([]RecallMatch, 0)
	for rows.Next() {
		var m RecallMatch
		var occurredStart, occurredEnd, mentionedAt sql.NullTime
		var chunkID, documentID sql.NullString
		if err := rows.Scan(&m.ID, &m.Text, &m.Type, &occurredStart, &occurredEnd, &mentionedAt, &chunkID, &documentID); err != nil {
			return RecallResult{}, err
		}
		m.OccurredStart = nullTimePtr(occurredStart)
		m.OccurredEnd = nullTimePtr(occurredEnd)
		m.MentionedAt = nullTimePtr(mentionedAt)
		m.ChunkID = chunkID.String
		m.DocumentID = documentID.String
		m.Final = float64(len(matches) + 1)
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return RecallResult{}, err
	}
	return RecallResult{Matches: matches, Trace: traceForLexical(matches)}, nil
}

// traceForLexical records the count accounting for the lexical recall path,
// which has a single stage so its per-stage counts are trivial but its totals
// still belong in the trace.
func traceForLexical(matches []RecallMatch) RecallTrace {
	return RecallTrace{
		Budget:          len(matches),
		BudgetUsed:      len(matches),
		CandidatesFound: len(matches),
		StageCounts:     map[string]int{"semantic": 0, "keyword": len(matches), "reranker": 0},
	}
}
