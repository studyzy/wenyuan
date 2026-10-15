package storage

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// BM25 query bounding.
//
// A native tsquery OR-joined from every word of a long query matches a large
// fraction of the bank, and ts_rank_cd — which has no IDF and is not
// index-backed — is then computed for every matched row, so ORDER BY ... LIMIT
// cannot prune before ranking. A broad query times out (the reference saw
// +60s scans). Two defences sit here: keeping only the most selective query
// terms, and a statement timeout as the backstop for the residual case term
// selection cannot catch (a term rare tenant-wide but hot in one bank).

// bm25MaxQueryTerms bounds how many query terms one BM25 tsquery carries. It
// matches the reference's default (HINDSIGHT_API_BM25_MAX_QUERY_TERMS = 16).
const bm25MaxQueryTerms = 16

// bm25StatementTimeout bounds the BM25 statement's server-side runtime. It is
// the backstop: term selection caps how much of the bank the query matches, but
// the corpus statistics it reads are tenant-global, so a term rare across the
// schema yet hot in a single bank can still drive a multi-second rank pass.
const bm25StatementTimeout = "10s"

// bm25TokenPattern splits a query into BM25 tokens: everything that is not a
// letter, digit or underscore becomes a separator, mirroring the reference's
// tokenizer so the two agree on how a query decomposes.
var bm25TokenPattern = regexp.MustCompile(`[^\p{L}\p{N}_]+`)

// tokenizeQuery lowercases and splits a query into BM25 tokens. A query with no
// word characters yields no tokens.
func tokenizeQuery(text string) []string {
	return strings.Fields(bm25TokenPattern.ReplaceAllString(strings.ToLower(text), " "))
}

// bm25TermStat is one query token's corpus statistics read from pg_stats. A
// token that produces no lexeme (a stopword) has hasLexeme false and is dropped
// from the selection; a token whose lexemes are absent from the statistics gets
// df 0, which reads as "rare" and is kept first — exactly what is wanted.
type bm25TermStat struct {
	df        float64
	hasLexeme bool
}

// selectSelectiveTerms returns at most maxTerms query tokens, preferring the
// lowest-document-frequency (most selective) ones, in their original order. A
// query already within the cap is returned unchanged. When the statistics are
// unusable (wrong length, or every token a stopword) it falls back to the first
// maxTerms tokens so selection can never block recall.
func selectSelectiveTerms(tokens []string, stats []bm25TermStat, maxTerms int) []string {
	if maxTerms <= 0 || len(tokens) <= maxTerms {
		return tokens
	}
	if len(stats) != len(tokens) {
		return tokens[:maxTerms]
	}
	type scored struct {
		df  float64
		ord int
	}
	candidates := make([]scored, 0, len(tokens))
	for ord, stat := range stats {
		if !stat.hasLexeme {
			continue
		}
		candidates = append(candidates, scored{df: stat.df, ord: ord})
	}
	if len(candidates) == 0 {
		return tokens[:maxTerms]
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].df == candidates[j].df {
			return candidates[i].ord < candidates[j].ord
		}
		return candidates[i].df < candidates[j].df
	})
	if len(candidates) > maxTerms {
		candidates = candidates[:maxTerms]
	}
	ords := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		ords = append(ords, candidate.ord)
	}
	// Return the kept tokens in their original relative order: the OR tsquery is
	// order-insensitive, but a stable order keeps traces and logs readable.
	sort.Ints(ords)
	kept := make([]string, 0, len(ords))
	for _, ord := range ords {
		kept = append(kept, tokens[ord])
	}
	return kept
}

// bm25TokenDocumentFrequencies reads each query token's maximum document
// frequency from the statistics PostgreSQL's ANALYZE maintains for the
// memory_units search vector, per lexeme, via pg_stats.most_common_elems. The
// result aligns with tokens by position. It is best-effort: a failure returns
// an error the caller treats as "fall back to the first-N cap".
func (s *PostgresStore) bm25TokenDocumentFrequencies(ctx context.Context, tokens []string) ([]bm25TermStat, error) {
	query := `WITH toks AS (
	              SELECT ord, tok
	              FROM unnest($1::text[]) WITH ORDINALITY AS u(tok, ord)
	          ),
	          lex AS (
	              SELECT t.ord, l.lexeme
	              FROM toks t
	              LEFT JOIN LATERAL unnest(tsvector_to_array(to_tsvector('english', t.tok))) AS l(lexeme) ON true
	          ),
	          stats AS (
	              SELECT unnest(most_common_elems::text::text[]) AS lexeme,
	                     unnest((most_common_elem_freqs)[1:array_length(most_common_elems::text::text[], 1)]) AS freq
	              FROM pg_stats
	              WHERE schemaname = $2 AND tablename = $3 AND attname = 'search_vector'
	          )
	          SELECT lex.ord AS ord,
	                 bool_or(lex.lexeme IS NOT NULL) AS has_lexeme,
	                 COALESCE(MAX(stats.freq), 0)::float8 AS df
	          FROM lex
	          LEFT JOIN stats ON stats.lexeme = lex.lexeme
	          GROUP BY lex.ord
	          ORDER BY lex.ord`
	rows, err := s.db.QueryContext(ctx, query, tokens, s.schema, "memory_units")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	stats := make([]bm25TermStat, len(tokens))
	for rows.Next() {
		var ord int
		var hasLexeme bool
		var df float64
		if err := rows.Scan(&ord, &hasLexeme, &df); err != nil {
			return nil, err
		}
		if ord >= 1 && ord <= len(stats) {
			stats[ord-1] = bm25TermStat{df: df, hasLexeme: hasLexeme}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// boundedBM25Text returns the text a BM25 arm binds: the query's tokens
// OR-joined into one tsquery (`tok1 | tok2 | ...`), so any term can match a
// memory rather than requiring every term. The reference builds its native
// BM25 arm the same way — a disjunctive candidate set whose precision is
// restored downstream by ranking, fusion and reranking — which is why a query
// like "where does Alice live?" still finds "Alice moved to Berlin".
//
// A query longer than the term cap is first reduced to its most selective
// terms, so a broad query cannot ask the ranker to score the whole bank. Term
// selection that cannot read the statistics falls back to a positional cap. An
// empty result means no term to search for.
func (s *PostgresStore) boundedBM25Text(ctx context.Context, term string) string {
	tokens := tokenizeQuery(term)
	if len(tokens) > bm25MaxQueryTerms {
		stats, err := s.bm25TokenDocumentFrequencies(ctx, tokens)
		if err != nil {
			tokens = tokens[:bm25MaxQueryTerms]
		} else {
			tokens = selectSelectiveTerms(tokens, stats, bm25MaxQueryTerms)
		}
	}
	return joinBM25Terms(tokens)
}

// joinBM25Terms renders tokens as a disjunctive tsquery body. Tokens carry only
// letters, digits and underscores (see tokenizeQuery), so no tsquery
// metacharacter can leak in and no quoting is needed.
func joinBM25Terms(tokens []string) string {
	return strings.Join(tokens, " | ")
}
