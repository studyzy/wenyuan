package storage

import "context"

// ClearBankMemories removes a bank's memory units and the rows that hang off
// them — links and entity associations — while leaving the bank, its entities
// and its documents in place. A non-empty factType narrows the removal to one
// memory type, which is what makes "clear only the observations" possible.
//
// The count is taken inside the same transaction as the delete, so a caller's
// before/after diff cannot race a concurrent retain.
func (s *PostgresStore) ClearBankMemories(ctx context.Context, bankID, factType string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	qualified := quoteIdent(s.schema)
	// An empty type matches every unit; otherwise the predicate narrows to one
	// fact type. The two forms are kept as separate statements so the type
	// filter is never passed as an empty-string comparison.
	typeFilter := ""
	args := []any{bankID}
	if factType != "" {
		typeFilter = " AND fact_type = $2"
		args = append(args, factType)
	}

	// Links and entity associations hold the foreign keys, so they go first:
	// a link may point at a unit that is about to disappear.
	for _, statement := range []string{
		`DELETE FROM ` + qualified + `.memory_links
		 WHERE from_unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1` + typeFilter + `)
		    OR to_unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1` + typeFilter + `)`,
		`DELETE FROM ` + qualified + `.unit_entities
		 WHERE unit_id IN (SELECT id FROM ` + qualified + `.memory_units WHERE bank_id = $1` + typeFilter + `)`,
	} {
		if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
			return 0, err
		}
	}

	// The unit delete decides the reported count. The subselect is used twice
	// for the link statements, but only here do the rows themselves go.
	deleteQuery := `DELETE FROM ` + qualified + `.memory_units WHERE bank_id = $1` + typeFilter
	result, err := tx.ExecContext(ctx, deleteQuery, args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(affected), nil
}
