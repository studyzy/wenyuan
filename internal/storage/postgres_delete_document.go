package storage

import (
	"context"
	"database/sql"
	"errors"
)

// DeleteDocument removes a document and the units extracted from it, returning
// the number of memory units removed.
//
// The whole cascade is one transaction. It first takes the bank row lock, which
// serializes bank-scoped deletes so two concurrent cascades whose documents
// share an observation cannot each hold a source the other's sweep needs. The
// sweep of observations left without a source runs both before and after the
// cascade: before, so the shared rows are locked in one order alongside this
// document's own; after, to catch an observation consolidation committed while
// the cascade ran.
func (s *PostgresStore) DeleteDocument(ctx context.Context, bankID, docID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	qualified := quoteIdent(s.schema)
	// NO KEY UPDATE, not FOR UPDATE: it still excludes a concurrent cascade but
	// stays compatible with the foreign-key checks on the bank's child rows.
	var lockedBank string
	if err := tx.QueryRowContext(ctx,
		`SELECT bank_id FROM `+qualified+`.banks WHERE bank_id = $1 FOR NO KEY UPDATE`,
		bankID).Scan(&lockedBank); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	unitIDs, err := unitIDsForDocumentTx(ctx, tx, qualified, bankID, docID)
	if err != nil {
		return 0, err
	}
	// An observation resting on any of these facts loses a source when they go,
	// so it is swept. The sweep takes the shared rows in one id order before the
	// cascade does, mirroring a re-ingest that sweeps first.
	if len(unitIDs) > 0 {
		if _, err := s.deleteStaleObservationsTx(ctx, tx, bankID, unitIDs); err != nil {
			return 0, err
		}
	}

	unitsAffected := 0
	unitsStatement := `DELETE FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2`
	documentStatement := `DELETE FROM ` + qualified + `.documents WHERE bank_id = $1 AND id = $2`
	// Links that reference a removed unit go first, because they hold the
	// foreign keys.
	for _, statement := range []string{
		`DELETE FROM ` + qualified + `.memory_links
		 WHERE from_unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)
		    OR to_unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)`,
		`DELETE FROM ` + qualified + `.unit_entities
		 WHERE unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)`,
		unitsStatement,
		`DELETE FROM ` + qualified + `.chunks WHERE bank_id = $1 AND document_id = $2`,
		// An attachment row is the document's own: the file bytes may be shared
		// with another document through the same hash, but this row names THIS
		// document, so it goes with it.
		`DELETE FROM ` + qualified + `.attachments WHERE bank_id = $1 AND document_id = $2`,
		documentStatement,
	} {
		result, err := tx.ExecContext(ctx, statement, bankID, docID)
		if err != nil {
			return 0, err
		}
		switch statement {
		case unitsStatement:
			affected, err := result.RowsAffected()
			if err != nil {
				return 0, err
			}
			unitsAffected = int(affected)
		case documentStatement:
			affected, err := result.RowsAffected()
			if err != nil {
				return 0, err
			}
			if affected == 0 {
				return 0, ErrDocumentNotFound
			}
		}
	}

	// A second sweep picks up an observation consolidation committed between the
	// first sweep and the cascade above.
	if len(unitIDs) > 0 {
		if _, err := s.deleteStaleObservationsTx(ctx, tx, bankID, unitIDs); err != nil {
			return 0, err
		}
	}

	if err := pruneOrphanEntities(ctx, tx, s.schema, bankID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return unitsAffected, nil
}

// stmtExecer is the statement-running subset both DB and Tx share, so a prune
// can run on a transaction or on the bare pool with one implementation.
type stmtExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// pruneOrphanEntities deletes the bank's entities that no surviving unit
// mentions. Their unit_entities and co-occurrence rows cascade with them. It is
// bank-wide because an entity may have been shared, and only the ones with no
// surviving mention go.
func pruneOrphanEntities(ctx context.Context, q stmtExecer, schema, bankID string) error {
	qualified := quoteIdent(schema)
	_, err := q.ExecContext(ctx,
		`DELETE FROM `+qualified+`.entities e
		 WHERE e.bank_id = $1
		   AND NOT EXISTS (SELECT 1 FROM `+qualified+`.unit_entities ue WHERE ue.entity_id = e.id)`,
		bankID)
	return err
}

// unitIDsForDocumentTx returns a document's unit ids in a deterministic order on
// the caller's transaction, so a delete can sweep the observations resting on
// them before the cascade removes the rows the query reads from.
func unitIDsForDocumentTx(ctx context.Context, tx Tx, qualified, bankID, docID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id::text FROM `+qualified+`.memory_units
		 WHERE bank_id = $1 AND document_id = $2 ORDER BY id ASC`, bankID, docID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
