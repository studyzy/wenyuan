package storage

import (
	"context"
	"errors"
	"fmt"
)

// ErrBackfillNeedsEmbeddings reports a reindex asked for with no embedding
// model. A search_vector could still be rebuilt, but a row left unembedded
// would look repaired while the semantic arm still could not rank it, so the
// run is refused rather than reported as a success.
var ErrBackfillNeedsEmbeddings = errors.New("backfill needs an embedding generator")

// BackfillReport is what one BackfillVectors run did.
type BackfillReport struct {
	BankID string
	// RowsRebuilt counts every row whose embedding and search_vector were
	// rewritten.
	RowsRebuilt int
}

// BackfillVectors recomputes the retrieval columns of a bank's existing rows.
//
// An archive carries no vectors: an import regenerates them from the
// deployment's own embedding model. A deployment whose embedding backend was
// down during the import, or which imported with WENYUAN_IMPORT_SKIP_EMBEDDINGS,
// therefore lands rows the semantic arm can never rank, and nothing repairs
// them afterwards — a write embeds only what it writes, and the maintenance
// jobs rebuild observations rather than vectors. This is that repair, and the
// same pass a deployment needs after changing its embedding model or its
// text-search dictionary, since a stored search_vector is built under the
// dictionary in force when the row was written.
//
// Rows are selected by what the running deployment would build today: an
// unembedded row, or one whose search_vector differs from what its own text
// and context produce under the configured dictionary. A second run therefore
// finds nothing to do, which is what makes it safe to repeat after a partial
// run.
//
// The text embedded is the row's text and context, without the entity names a
// retain prefixes it with: a stored row carries no resolved entity names, so a
// reindex restores a row's visibility rather than reproducing its write.
func (s *PostgresStore) BackfillVectors(ctx context.Context, bankID string, embed EmbeddingGenerator) (BackfillReport, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return BackfillReport{}, schemaErr
	}
	if embed == nil {
		return BackfillReport{}, ErrBackfillNeedsEmbeddings
	}
	qualified := quoteIdent(schema) + ".memory_units"
	// The same expression UpsertUnit writes, so a row this pass rebuilds and a
	// row a later write stores cannot disagree about what the column holds.
	vector := s.searchVectorExpr("text", "context", "text_signals")
	predicate := `embedding IS NULL OR search_vector IS NULL OR search_vector != ` + vector
	report := BackfillReport{BankID: bankID}

	selectQuery := `SELECT id::text, text, context
	                  FROM ` + qualified + `
	                  WHERE bank_id = $1 AND (` + predicate + `)
	                  ORDER BY id LIMIT $2`
	updateQuery := `UPDATE ` + qualified + `
	                    SET embedding = $2::vector, search_vector = ` + vector + `
	                    WHERE id = $1::uuid`
	// The loop pages by what is still unmatched rather than by an offset, so a
	// rewritten row that no longer matches drops out and the next batch starts
	// where the previous one ended. An interruption therefore leaves nothing to
	// clean up: a second run simply finds the rows the first did not reach.
	for {
		rows, err := s.reader().QueryContext(ctx, selectQuery, bankID, defaultBackfillBatchSize)
		if err != nil {
			return report, fmt.Errorf("select rows to backfill: %w", err)
		}
		type pending struct {
			id   string
			text string
		}
		batch := make([]pending, 0, defaultBackfillBatchSize)
		for rows.Next() {
			var row pending
			var text, contextText *string
			if err := rows.Scan(&row.id, &text, &contextText); err != nil {
				_ = rows.Close()
				return report, fmt.Errorf("scan row to backfill: %w", err)
			}
			row.text = joinText(text, contextText)
			batch = append(batch, row)
		}
		readErr := rows.Err()
		_ = rows.Close()
		if readErr != nil {
			return report, fmt.Errorf("read rows to backfill: %w", readErr)
		}
		if len(batch) == 0 {
			return report, nil
		}
		texts := make([]string, len(batch))
		for i, row := range batch {
			texts[i] = row.text
		}
		vectors, err := embed.EmbedPassages(ctx, texts)
		if err != nil {
			return report, fmt.Errorf("embed %d rows of bank %q: %w", len(batch), bankID, err)
		}
		if len(vectors) != len(batch) {
			return report, fmt.Errorf("embedding generator returned %d vectors for %d texts", len(vectors), len(batch))
		}
		for i, row := range batch {
			if err := ValidateEmbedding(vectors[i]); err != nil {
				return report, fmt.Errorf("embed row %s: %w", row.id, err)
			}
			if _, err := s.db.ExecContext(ctx, updateQuery, row.id, EncodeEmbedding(vectors[i])); err != nil {
				return report, fmt.Errorf("update row %s: %w", row.id, err)
			}
			report.RowsRebuilt++
		}
	}
}

// defaultBackfillBatchSize bounds one embed request of a reindex. The generator
// splits further by its provider's own limit; this only keeps the statement and
// its result set small.
const defaultBackfillBatchSize = 32

// joinText is the text a reindex embeds: a row's text and its context. The
// search_vector is built in SQL from the stored columns, so it carries the
// text signals too, and this pairing only has to match what a write embeds.
func joinText(text, contextText *string) string {
	joined := derefString(text)
	if context := derefString(contextText); context != "" {
		joined = joined + " " + context
	}
	return joined
}

// derefString reads an optional column as a plain string.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
