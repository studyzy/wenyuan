package storage

import "strings"

// The lexical arm's two forms.
//
// The native form (see keywordHits) ranks tsvector matches; the pg_search form
// ranks ParadeDB's own BM25 index, whose score is read by calling a function on
// the key column and whose match gate is the `@@@` operator over a
// `boolean(should => ...)` array. The two differ in exactly the places the
// reference's build_bm25_arm branches on (engine/sql/postgresql.py), plus the
// tie-break both forms carry so an arm's reported rank is reproducible.

// pgSearchShould renders the `should => ...` array a pg_search arm matches on.
//
// With a configured tokenizer the query is tokenized in SQL by the same
// tokenizer the index was built with, and only its distinct word tokens are
// searched — multi-character words first (a single CJK character is usually a
// particle), capped at maxQueryTerms — as exact term queries over each field.
// Matching the raw sentence instead ORs every token including whitespace and
// punctuation, which fans a long query out over the whole index.
//
// An ngram tokenizer emits overlapping grams, so pruning its tokens would keep
// only the query's first few characters; it — and the default tokenizer, which
// keeps a CJK run as one token — gets the raw `match` form instead. tokenizer is
// the normalized whitelist value from resolveTextSearchBackend, which is why it
// is safe to embed.
func pgSearchShould(fn string, fields []string, textParam, tokenizer string, maxQueryTerms int) string {
	if tokenizer != "" && !strings.HasPrefix(tokenizer, "ngram") && !strings.HasPrefix(tokenizer, "edge_ngram") {
		cap := ""
		if maxQueryTerms > 0 {
			cap = " LIMIT " + itoa(maxQueryTerms)
		}
		values := make([]string, 0, len(fields))
		for _, field := range fields {
			values = append(values, "("+sqlTextLiteral(field)+")")
		}
		return "ARRAY(SELECT " + fn + ".term(f, t) FROM (" +
			"SELECT t FROM unnest(" + textParam + "::text::pdb." + tokenizer + "::text[]) WITH ORDINALITY u(t, o)" +
			" WHERE t !~ '^[[:space:][:punct:]]*$'" +
			" GROUP BY t ORDER BY char_length(t) > 1 DESC, min(o)" + cap +
			") k CROSS JOIN (VALUES " + strings.Join(values, ", ") + ") v(f))"
	}
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		terms = append(terms, fn+".match("+sqlTextLiteral(field)+", "+textParam+")")
	}
	return "ARRAY[" + strings.Join(terms, ", ") + "]"
}

// pgSearchArmFields are the columns the pg_search arm matches over. `context` is
// deliberately excluded: it multiplies the postings scanned for little recall
// signal (the reference's #4313).
var pgSearchArmFields = []string{"text", "text_signals"}

// bm25ArmQuery renders the keyword arm's statement and its arguments.
//
// The shape is identical between the two backends — the same columns, the same
// positional contract ($1 bank, $2 types, $3 query text, $4 limit) and the same
// ctid tie-break — so only the score expression and the match gate change.
func (s *PostgresStore) bm25ArmQuery(q RecallQuery, term string, limit int) (string, []any) {
	args := append(armArgs(q), term, limit)
	head := `SELECT id::text, text, fact_type, occurred_start, occurred_end, mentioned_at, chunk_id, document_id, context, metadata, tags,
	                 COALESCE(source_memory_ids, '{}'::uuid[])::text, `
	tail := `
	          FROM ` + quoteIdent(s.schema) + `.memory_units
	          WHERE bank_id = $1 AND fact_type = ANY($2) AND ` + recallPredicates(q) + `
	            AND `
	if s.textSearchExtension == TextSearchPGSearch {
		fn := s.pgFunctionSchema
		should := pgSearchShould(fn, pgSearchArmFields, "$3", s.pgTokenizer, s.bm25MaxQueryTerms())
		return head + fn + `.score(id) AS score` + tail + `id @@@ ` + fn + `.boolean(should => ` + should + `)` + `
	          ORDER BY score DESC, ctid ASC LIMIT $4`, args
	}
	return head + `ts_rank_cd(search_vector, to_tsquery(` + sqlTextLiteral(s.nativeLanguage()) + `, $3)) AS score` + tail +
		`search_vector @@ to_tsquery(` + sqlTextLiteral(s.nativeLanguage()) + `, $3)` + `
	          ORDER BY score DESC, ctid ASC LIMIT $4`, args
}

// bm25MaxQueryTerms is the resolved cap on how many query terms one BM25 arm
// carries, falling back to the reference's default when the tuning was not
// resolved.
func (s *PostgresStore) bm25MaxQueryTerms() int {
	if s.tuning.BM25MaxQueryTerms > 0 {
		return s.tuning.BM25MaxQueryTerms
	}
	return DefaultBM25MaxQueryTerms
}
