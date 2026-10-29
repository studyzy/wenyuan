package storage

import (
	"context"
	"strings"
)

// renameBankTables lists every table whose rows are scoped to one bank, in the
// order the rename moves them. The banks row itself is handled separately
// because its identifier is the primary key the children reference.
var renameBankTables = []string{
	"bank_aliases",
	"async_operations",
	"attachments",
	"audit_log",
	"bank_stats_cache",
	"chunks",
	"directives",
	"documents",
	"entities",
	"entity_maintenance_queue",
	"graph_maintenance_queue",
	"invalidated_memory_units",
	"knowledge_pages",
	"llm_requests",
	"memory_links",
	"memory_units",
	"mental_model_history",
	"mental_models",
	"observation_history",
	"webhooks",
}

// RenameBank moves a bank and every row scoped to it from source to target in
// one transaction, so a rename preserves the bank's data rather than recreating
// it empty.
//
// The target bank row is inserted first, then every scoped row is re-keyed, and
// finally the source row is removed. Inserting before moving keeps every
// child's foreign key satisfied without relying on deferred constraints, which
// most of the bank_id foreign keys are not.
func (s *PostgresStore) RenameBank(ctx context.Context, source, target string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if !ValidBankID(target) {
		return ErrInvalidBankID
	}
	if source == target {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	qualified := quoteIdent(schema)

	exists, err := bankExistsTx(ctx, tx, qualified, source)
	if err != nil {
		return err
	}
	if !exists {
		return ErrBankNotFound
	}
	targetExists, err := bankExistsTx(ctx, tx, qualified, target)
	if err != nil {
		return err
	}
	if targetExists {
		return ErrBankExists
	}

	// Copy the banks row under the target identifier. The column list is read
	// from the catalogue so a schema change does not silently drop a column
	// from the copy.
	columns, err := bankColumnsTx(ctx, tx, schema)
	if err != nil {
		return err
	}
	copied := make([]string, 0, len(columns))
	for _, column := range columns {
		if column != "bank_id" {
			copied = append(copied, quoteIdent(column))
		}
	}
	insert := `INSERT INTO ` + qualified + `.banks (` + quoteIdent("bank_id") + `, ` + strings.Join(copied, ", ") + `)
	          SELECT $1, ` + strings.Join(copied, ", ") + ` FROM ` + qualified + `.banks WHERE bank_id = $2`
	if _, err := tx.ExecContext(ctx, insert, target, source); err != nil {
		return err
	}

	for _, table := range renameBankTables {
		update := `UPDATE ` + qualified + `.` + quoteIdent(table) + ` SET bank_id = $1 WHERE bank_id = $2`
		if _, err := tx.ExecContext(ctx, update, target, source); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM `+qualified+`.banks WHERE bank_id = $1`, source); err != nil {
		return err
	}
	return tx.Commit()
}

// bankExistsTx reports whether a bank row exists, within the caller's
// transaction.
func bankExistsTx(ctx context.Context, tx Tx, qualified, bankID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM `+qualified+`.banks WHERE bank_id = $1)`, bankID).Scan(&exists)
	return exists, err
}

// bankColumnsTx reads the banks table's column names, in ordinal order, so the
// copy addresses the columns the deployed schema actually has.
func bankColumnsTx(ctx context.Context, tx Tx, schema string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = 'banks'
		 ORDER BY ordinal_position`, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}
