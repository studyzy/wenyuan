package storage

import (
	"context"
)

// The pg_search keyword index.
//
// The native backend ranks a tsvector column with ts_rank_cd, and that column
// is built by PostgreSQL's own parser. For Chinese that parser is not a
// tokenizer: it keeps a contiguous run of CJK characters as a single lexeme, so
// a row reading "爱丽丝搬到了柏林" is reachable only by that exact run and never
// by "柏林". ParadeDB's pg_search replaces both halves — its `jieba` tokenizer
// splits the text at index time and at query time — and that is the only way a
// sub-sentence Chinese term survives into the keyword arm.
//
// The index is a runtime object rather than part of the migration series. The
// series is generated from the reference's catalogue and ExpectedSchemaOf()
// derives the expected object set from it, so an index placed there would be
// read as something the reference schema contains. It is instead reconciled
// beside the per-bank vector indexes, and only when the deployment selected the
// pg_search backend: a native deployment has no pg_search to call, and
// PostgreSQL would reject the access method outright.

// bm25IndexName is the single keyword index over a tenant schema's memory_units.
//
// One index serves the whole bank rather than one per bank: a bm25 index is
// cheap relative to the per-bank vector indexes, and the arm already filters by
// bank_id, so a per-bank split would multiply the index count for no gain.
const bm25IndexName = "idx_mu_bm25"

// BM25IndexDDL renders the statement that builds the keyword index in one
// schema, or "" when the deployment is not on the pg_search backend.
//
// Each searched column is cast to the configured tokenizer, which is how
// pg_search 0.26 accepts a tokenizer: the deprecated `tokenizer=` index
// parameter was removed, and `(text::pdb.jieba)` is the spelling that survives.
// The key column is `id`, pg_search's row identifier, and it no longer needs to
// be named as of 0.26 — it is the table's primary key.
//
// The statement is CONCURRENTLY and runs outside a transaction: a plain CREATE
// INDEX takes a write-blocking lock on memory_units, and the first build over a
// populated schema can take a long time.
func BM25IndexDDL(schema, tokenizer string) string {
	if tokenizer == "" || tokenizer == TextSearchNative {
		return ""
	}
	casts := ""
	for i, field := range pgSearchArmFields {
		if i > 0 {
			casts += ", "
		}
		casts += "(" + quoteIdent(field) + "::pdb." + tokenizer + ")"
	}
	return "CREATE INDEX CONCURRENTLY IF NOT EXISTS " + quoteIdent(bm25IndexName) +
		" ON " + quoteIdent(schema) + ".memory_units USING bm25 (" + casts + ")"
}

// EnsureBM25Index builds the keyword index for the schema the store is bound
// to. It is a no-op on the native backend, where there is no pg_search index to
// build, and on schemas whose memory_units table does not exist yet.
//
// A build failure is returned to the caller rather than swallowed: a pg_search
// deployment whose index is missing answers the keyword arm with a sequential
// scan, which is not an error the query path can see.
func (s *PostgresStore) EnsureBM25Index(ctx context.Context) error {
	if s.textSearchExtension != TextSearchPGSearch {
		return nil
	}
	schema, err := s.schemaFor(ctx)
	if err != nil {
		return err
	}
	stmt := BM25IndexDDL(schema, s.pgTokenizer)
	if stmt == "" {
		return nil
	}
	// The primary, not reader(): a read replica cannot take DDL, and the index
	// has to exist where the writes land.
	_, err = s.db.ExecContext(ctx, stmt)
	return err
}
