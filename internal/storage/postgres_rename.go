package storage

import (
	"context"
	"fmt"
	"strconv"
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
	"llm_requests",
	"memory_links",
	"memory_units",
	"mental_model_history",
	"mental_models",
	"knowledge_pages",
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
	columns, err := bankColumnsWithoutDefaultsTx(ctx, tx, schema)
	if err != nil {
		return err
	}
	copied := bankNameCarriedFromID(columns, "$1", "$2")
	insert := `INSERT INTO ` + qualified + `.banks (` + quoteIdent("bank_id") + `, ` +
		strings.Join(bankNameInsertColumns(columns), ", ") + `)
	          SELECT $1, ` + strings.Join(copied, ", ") + ` FROM ` + qualified + `.banks WHERE bank_id = $2`
	// A bank-scoped table can reference another by a composite (id, bank_id), as
	// chunks does documents. Those foreign keys are immediate, so one side
	// cannot move before the other: PostgreSQL checks rows, not statements, and
	// no ordering of two UPDATEs leaves every check satisfied. Such constraints
	// are deferred to the commit for the duration of this transaction, which is
	// exactly what makes the move atomic — both sides change inside it, and the
	// check then sees only the finished state.
	//
	// The copy goes in after the keys are deferred: inserting it touches the
	// banks row every bank-scoped composite key names, so an immediate key
	// would reject the insert while the children still point at the source.
	deferred, err := makeCompositeBankKeysDeferrableTx(ctx, tx, schema)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insert, target, source); err != nil {
		return err
	}
	for _, table := range renameBankTables {
		columns, err := tableBankReferencesTx(ctx, tx, schema, table)
		if err != nil {
			return err
		}
		update := `UPDATE ` + qualified + `.` + quoteIdent(table) + ` SET ` +
			rekeyAssignments(columns, 1) + ` WHERE ` + rekeyFilter(columns, 1)
		args := rekeyArgs([][2]string{{source, target}})
		if _, err := tx.ExecContext(ctx, update, args...); err != nil {
			return fmt.Errorf("re-key %s: %w", table, err)
		}
	}
	if err := restoreCompositeBankKeysTx(ctx, tx, schema, deferred); err != nil {
		return err
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

// compositeBankKey is one foreign key over more than one column that names a
// bank, recorded by its table and name so it can be restored afterwards.
type compositeBankKey struct {
	table string
	name  string
	// columns is the child's attnum list, comma-separated, in key order.
	columns string
	// match is the referenced table and its column list, e.g.
	// `studyzy.documents(id, bank_id)`.
	match string
	// matchOption is pg_constraint.confmatchtype: `f`, `p` or `s`, rendered by
	// matchClause.
	matchOption string
	update      string
	delete      string
}

// matchClause renders the MATCH option of a foreign key, if it is not the
// default.
func (k compositeBankKey) matchClause() string {
	switch k.matchOption {
	case "f":
		return " MATCH FULL"
	case "p":
		return " MATCH PARTIAL"
	case "s":
		return " MATCH SIMPLE"
	default:
		return ""
	}
}

// makeCompositeBankKeysDeferrableTx turns this schema's composite bank foreign
// keys into deferrable ones for the length of the transaction, and returns
// what it changed so the caller can put them back.
//
// A composite key is what couples two bank-scoped tables: chunks references
// documents by (document_id, bank_id), so moving either side alone leaves the
// other's rows dangling. PostgreSQL checks such a key when the row is written,
// and `SET CONSTRAINTS ... DEFERRED` is refused on a key declared NOT
// DEFERRABLE — so the keys are re-declared deferrable, the moves run, the
// commit checks them once over the finished state, and the re-declaration is
// reverted. Both the flip and the revert are transactional DDL: a rollback
// undoes them along with the moves.
func makeCompositeBankKeysDeferrableTx(ctx context.Context, tx Tx, schema string) ([]compositeBankKey, error) {
	// The referenced column list keeps its schema qualification so the
	// re-declaration stays unambiguous; the ON DELETE action is re-emitted
	// explicitly because a dropped NOT DEFERRABLE constraint restated without
	// one would default to NO ACTION.
	rows, err := tx.QueryContext(ctx,
		`SELECT t.relname, c.conname,
		        array_to_string(c.conkey, ','),
		        format('%I.%I', rn.nspname, rt.relname) || '(' ||
		        (SELECT string_agg(quote_ident(pa.attname), ', ' ORDER BY i.i)
		         FROM pg_attribute pa, generate_subscripts(c.confkey, 1) AS i(i)
		         WHERE pa.attrelid = c.confrelid AND pa.attnum = c.confkey[i.i]) || ')',
		        c.confmatchtype,
		        c.confupdtype, c.confdeltype
		 FROM pg_constraint c
		 JOIN pg_class t ON t.oid = c.conrelid
		 JOIN pg_namespace n ON n.oid = t.relnamespace
		 JOIN pg_class rt ON rt.oid = c.confrelid
		 JOIN pg_namespace rn ON rn.oid = rt.relnamespace
		 WHERE c.contype = 'f' AND n.nspname = $1
		   AND cardinality(c.conkey) > 1 AND NOT c.condeferrable
		 ORDER BY t.relname, c.conname`, schema)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []compositeBankKey
	for rows.Next() {
		var key compositeBankKey
		var onUpdate, onDelete string
		if err := rows.Scan(&key.table, &key.name, &key.columns, &key.match, &key.matchOption, &onUpdate, &onDelete); err != nil {
			return nil, err
		}
		key.update = foreignKeyAction(onUpdate)
		key.delete = foreignKeyAction(onDelete)
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range keys {
		key := &keys[i]
		if err := replaceBanksKey(ctx, tx, schema, key, true); err != nil {
			return nil, err
		}
	}
	if len(keys) > 0 {
		if _, err := tx.ExecContext(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

// restoreCompositeBankKeysTx puts the keys back to NOT DEFERRABLE, after
// forcing their deferred check while PostgreSQL still holds this transaction
// open.
func restoreCompositeBankKeysTx(ctx context.Context, tx Tx, schema string, keys []compositeBankKey) error {
	if len(keys) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return err
	}
	for i := range keys {
		if err := replaceBanksKey(ctx, tx, schema, &keys[i], false); err != nil {
			return err
		}
	}
	return nil
}

// columnList renders the child's columns of the key, in key order, from the
// attnum list the catalogue records.
//
// The order comes from the caller, not from the query: the key's column order
// is what pairs each column with its referenced one, and an aggregate without
// an ORDER BY (or one over a set the planner reorders) silently permutes it.
func (k compositeBankKey) columnList(ctx context.Context, tx Tx, schema string) (string, error) {
	names := make([]string, 0, 2)
	for _, field := range strings.Split(k.columns, ",") {
		attnum, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			return "", fmt.Errorf("column ordinal %q: %w", field, err)
		}
		var name string
		err = tx.QueryRowContext(ctx,
			`SELECT attname FROM pg_attribute WHERE attrelid = $1::regclass AND attnum = $2`,
			quoteIdent(schema)+"."+quoteIdent(k.table), attnum).Scan(&name)
		if err != nil {
			return "", err
		}
		names = append(names, quoteIdent(name))
	}
	return strings.Join(names, ", "), nil
}

// replaceBanksKey re-declares one composite bank foreign key, keeping its
// column pair, its referenced unique key and its referential actions.
func replaceBanksKey(ctx context.Context, tx Tx, schema string, key *compositeBankKey, deferrable bool) error {
	qualified := quoteIdent(schema) + "." + quoteIdent(key.table)
	drop := `ALTER TABLE ` + qualified + ` DROP CONSTRAINT ` + quoteIdent(key.name)
	if _, err := tx.ExecContext(ctx, drop); err != nil {
		return err
	}
	timing := " NOT DEFERRABLE"
	if deferrable {
		timing = " DEFERRABLE INITIALLY DEFERRED"
	}
	columns, err := key.columnList(ctx, tx, schema)
	if err != nil {
		return err
	}
	create := `ALTER TABLE ` + qualified + ` ADD CONSTRAINT ` + quoteIdent(key.name) +
		` FOREIGN KEY (` + columns + `) REFERENCES ` + key.match + key.matchClause() +
		` ON UPDATE ` + key.update + ` ON DELETE ` + key.delete + timing
	_, err = tx.ExecContext(ctx, create)
	return err
}

// foreignKeyAction renders a pg_constraint action code as the DDL keyword.
func foreignKeyAction(code string) string {
	switch code {
	case "r":
		return "RESTRICT"
	case "c":
		return "CASCADE"
	case "n":
		return "SET NULL"
	case "d":
		return "SET DEFAULT"
	default:
		return "NO ACTION"
	}
}

// tableBankReferencesTx names the columns of a bank-scoped table that hold a
// bank identifier. Most tables carry only `bank_id`; `memory_links` also holds
// both link endpoints' banks, and a composite foreign key such as
// chunks(document_id, bank_id) -> documents(id, bank_id) is checked as soon as
// either side of it is re-keyed.
func tableBankReferencesTx(ctx context.Context, tx Tx, schema, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = $2 AND column_name LIKE '%bank_id'
		 ORDER BY ordinal_position`, schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := make([]string, 0, 1)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("table %q carries no bank identifier column", table)
	}
	return columns, nil
}

// rekeyAssignments renders `c = CASE c WHEN $1 THEN $2 ... ELSE c END` for
// each column in a multi-bank rekey, whose pairs are bound densely from $1.
func rekeyAssignments(columns []string, pairs int) string {
	assignments := make([]string, 0, len(columns))
	when := make([]string, 0, pairs)
	for pair := 0; pair < pairs; pair++ {
		when = append(when, fmt.Sprintf("WHEN $%d THEN $%d", 2*pair+1, 2*pair+2))
	}
	for _, column := range columns {
		quoted := quoteIdent(column)
		assignments = append(assignments, quoted+` = CASE `+quoted+` `+strings.Join(when, " ")+` ELSE `+quoted+` END`)
	}
	return strings.Join(assignments, ", ")
}

// rekeyFilter renders the OR predicate that picks the rows the assignments
// touch: any of the columns naming any of the sources.
func rekeyFilter(columns []string, pairs int) string {
	filters := make([]string, 0, len(columns))
	for _, column := range columns {
		sources := make([]string, 0, pairs)
		for pair := 0; pair < pairs; pair++ {
			sources = append(sources, fmt.Sprintf("$%d", 2*pair+1))
		}
		filters = append(filters, quoteIdent(column)+` IN (`+strings.Join(sources, ", ")+`)`)
	}
	return strings.Join(filters, " OR ")
}

// rekeyArgs flattens the pairs as the statement binds them: source, target per
// pair, in the order the placeholders were rendered.
func rekeyArgs(pairs [][2]string) []any {
	args := make([]any, 0, 2*len(pairs))
	for _, pair := range pairs {
		args = append(args, pair[0], pair[1])
	}
	return args
}

// bankColumnsWithoutDefaultsTx reads the banks table's column names, in ordinal
// order, so the copy addresses the columns the deployed schema actually has.
//
// A column with a default is left out of the copy so the database assigns a
// fresh value. `internal_id` is such a column, and it is unique across the
// schema: copying the source's uuid onto the new row writes the same value
// twice — once live, once as the source row this rename is about to delete —
// and that pair violates the constraint even though the identifier only ever
// names one bank. A column without a default is carried over verbatim, which is
// what preserves the bank's own settings.
func bankColumnsWithoutDefaultsTx(ctx context.Context, tx Tx, schema string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_schema = $1 AND table_name = 'banks' AND column_default IS NULL
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
