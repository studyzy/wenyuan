package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// embeddingTables are the tables whose `embedding` column stores provider
// vectors. The generated series declares the width of whichever reference
// deployment it was dumped from, which is a deployment setting upstream rather
// than a property of the schema: the reference resolves the column width from
// the configured embedding model at startup and alters the column when it
// disagrees. This service therefore does the same instead of hand-editing the
// generated series, so regenerating the series cannot silently change the
// stored width.
var embeddingTables = []string{"memory_units", "mental_models"}

// reconcileEmbeddingColumns brings every embedding column to the width this
// service's provider boundary declares, inside the migration transaction and
// after the series has been applied or adopted.
//
// The three outcomes mirror the reference's own startup reconciliation:
//
//   - the column already has the declared width: nothing to do;
//   - it disagrees and the table holds embedded rows: refuse with the two
//     widths and the row count named, because changing the width would
//     invalidate every stored vector and the reference refuses for the same
//     reason instead of truncating or deleting;
//   - it disagrees on an empty table: drop the column's vector indexes, alter
//     the column to the declared width, and rebuild each index from the
//     definition captured before the drop — except a global (non-partial)
//     vector index on memory_units, which upstream keeps deliberately absent
//     and whose drop therefore stays final.
//
// A database this service adopts can have been written by any deployment, so a
// disagreement is a configuration fact to report, never a silent conversion.
func (s *PostgresStore) reconcileEmbeddingColumns(ctx context.Context, tx vectorSchemaTx) error {
	for _, table := range embeddingTables {
		width, err := embeddingColumnWidth(ctx, tx, s.defaultSchema, table)
		if errors.Is(err, sql.ErrNoRows) {
			// The table is absent (a partial adoption) or has no embedding
			// column; there is nothing to reconcile.
			continue
		}
		if err != nil {
			return err
		}
		if width == EmbeddingDimensions {
			continue
		}
		embedded, err := countEmbeddedRows(ctx, tx, s.defaultSchema, table)
		if err != nil {
			return err
		}
		if embedded > 0 {
			return fmt.Errorf(
				"embedding dimension mismatch on %s: the database stores vector(%d), this service stores vector(%d), "+
					"and the table holds %d embedded rows; re-embed the data (delete the rows and restart) or point "+
					"this service at %d-dimensional vectors",
				table, width, EmbeddingDimensions, embedded, width)
		}
		indexes, err := embeddingIndexDefinitions(ctx, tx, s.defaultSchema, table)
		if err != nil {
			return err
		}
		for _, def := range indexes {
			if _, err := tx.ExecContext(ctx, "DROP INDEX "+quoteIdent(s.defaultSchema)+"."+quoteIdent(def.name)); err != nil {
				return fmt.Errorf("drop %s before resizing %s.embedding: %w", def.name, table, err)
			}
		}
		alter := fmt.Sprintf("ALTER TABLE %s ALTER COLUMN embedding TYPE vector(%d)",
			quoteIdent(s.defaultSchema)+"."+quoteIdent(table), EmbeddingDimensions)
		if _, err := tx.ExecContext(ctx, alter); err != nil {
			return fmt.Errorf("resize %s.embedding to vector(%d): %w", table, EmbeddingDimensions, err)
		}
		// The captured definition carries the original dimension in its operator
		// class, not the column type, so replaying it rebuilds the index over the
		// resized column. An index the definition cannot be read for is reported
		// rather than dropped silently: losing a vector index would turn every
		// recall into a sequential scan.
		for _, def := range indexes {
			// Upstream deliberately keeps no global (non-partial) vector index on
			// memory_units: per-bank backends serve every vector search from the
			// per-bank partial indexes owned by the maintenance sweep (#5707), and
			// its repair migration e8c1a4f7b2d9 drops any stale global one by
			// catalogue lookup. Replaying the captured definition would recreate
			// exactly what upstream drops, so here the drop stays final. Partial
			// per-bank indexes still replay, as do indexes on other tables
			// (mental_models keeps its global index upstream).
			if table == "memory_units" && def.globalVectorIndex() {
				continue
			}
			if _, err := tx.ExecContext(ctx, def.definition); err != nil {
				return fmt.Errorf("rebuild %s after resizing %s.embedding: %w", def.name, table, err)
			}
		}
	}
	return nil
}

// embeddingColumnWidth reports the declared width of a table's embedding
// column, read from the catalogue rather than from the series so an adopted
// database is described as it actually is. sql.ErrNoRows means the table or
// column is absent.
func embeddingColumnWidth(ctx context.Context, tx vectorSchemaTx, schema, table string) (int, error) {
	var width int
	err := tx.QueryRowContext(ctx, `
		SELECT a.atttypmod
		  FROM pg_attribute a
		  JOIN pg_class c ON c.oid = a.attrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2 AND a.attname = 'embedding'
		   AND a.attnum > 0 AND NOT a.attisdropped`, schema, table).Scan(&width)
	return width, err
}

// countEmbeddedRows counts the rows whose embedding is present, which is the
// condition the reference uses to decide between altering and refusing.
func countEmbeddedRows(ctx context.Context, tx vectorSchemaTx, schema, table string) (int, error) {
	var rows int
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE embedding IS NOT NULL", quoteIdent(schema)+"."+quoteIdent(table))
	if err := tx.QueryRowContext(ctx, query).Scan(&rows); err != nil {
		return 0, fmt.Errorf("count embedded rows of %s: %w", table, err)
	}
	return rows, nil
}

// vectorSchemaTx is the slice of database/sql the reconcile needs, satisfied by
// both a transaction (the migration path) and the pool (a test reading the
// catalogue directly).
type vectorSchemaTx interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// capturedIndex is an index definition read before a drop so it can be replayed.
type capturedIndex struct {
	name       string
	definition string
	// amname is the index's access method and partial whether its definition
	// carries a WHERE clause, both read from the catalogue so the replay can
	// tell a global vector index from a per-bank partial one the way the
	// reference does.
	amname  string
	partial bool
}

// vectorAccessMethods are the access methods vector indexes are built with,
// the set the reference matches when it looks vector indexes up in the
// catalogue (migrations.py `_vector_index_names`, alembic e8c1a4f7b2d9).
var vectorAccessMethods = map[string]bool{"hnsw": true, "vchordrq": true, "diskann": true, "scann": true}

// globalVectorIndex reports whether a captured index is a global (non-partial)
// vector index, by the reference's own catalogue predicate: indpred IS NULL (no
// WHERE clause) and one of the vector access methods.
func (c capturedIndex) globalVectorIndex() bool {
	return !c.partial && vectorAccessMethods[c.amname]
}

// embeddingIndexDefinitions returns the definitions of the indexes on a table's
// embedding column. The definition is read from the catalogue rather than
// restated, so an index this service did not create (an adopted database's) is
// rebuilt in its own shape.
func embeddingIndexDefinitions(ctx context.Context, tx vectorSchemaTx, schema, table string) ([]capturedIndex, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT ic.relname, pg_get_indexdef(i.indexrelid), am.amname, i.indpred IS NOT NULL
		  FROM pg_index i
		  JOIN pg_class ic ON ic.oid = i.indexrelid
		  JOIN pg_class tc ON tc.oid = i.indrelid
		  JOIN pg_namespace n ON n.oid = tc.relnamespace
		  JOIN pg_am am ON am.oid = ic.relam
		  JOIN pg_attribute a ON a.attrelid = tc.oid AND a.attnum = ANY (i.indkey)
		 WHERE n.nspname = $1 AND tc.relname = $2 AND a.attname = 'embedding'
		 ORDER BY ic.relname`, schema, table)
	if err != nil {
		return nil, fmt.Errorf("list embedding indexes of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var out []capturedIndex
	for rows.Next() {
		var idx capturedIndex
		if err := rows.Scan(&idx.name, &idx.definition, &idx.amname, &idx.partial); err != nil {
			return nil, err
		}
		out = append(out, idx)
	}
	return out, rows.Err()
}
