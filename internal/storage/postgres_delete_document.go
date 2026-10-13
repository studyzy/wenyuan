package storage

import "context"

// DeleteDocument removes a document and the units extracted from it.
//
// The units are deleted in the same transaction as the document, so a failure
// cannot leave retrievable evidence whose source is gone.
func (s *PostgresStore) DeleteDocument(ctx context.Context, bankID, docID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	qualified := quoteIdent(s.schema)
	// Links that reference a removed unit go first, because they hold the
	// foreign keys.
	for _, statement := range []string{
		`DELETE FROM ` + qualified + `.memory_links
		 WHERE from_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)
		    OR to_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)`,
		`DELETE FROM ` + qualified + `.unit_entities
		 WHERE unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2)`,
		`DELETE FROM ` + qualified + `.memory_units WHERE bank_id = $1 AND document_id = $2`,
		`DELETE FROM ` + qualified + `.chunks WHERE bank_id = $1 AND document_id = $2`,
		`DELETE FROM ` + qualified + `.documents WHERE bank_id = $1 AND id = $2`,
	} {
		result, err := tx.ExecContext(ctx, statement, bankID, docID)
		if err != nil {
			return err
		}
		// Only the document delete decides whether anything was found.
		if statement == `DELETE FROM `+qualified+`.documents WHERE bank_id = $1 AND id = $2` {
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected == 0 {
				return ErrDocumentNotFound
			}
		}
	}
	return tx.Commit()
}
