package storage

import (
	"context"
	"fmt"
	"strings"
)

// The PostgreSQL implementation of AdminTableStore. It moves a whole table
// through JSON in one statement per direction: `row_to_json` on the way out and
// `json_populate_recordset` on the way in. Neither needs a column-by-column
// hand-written mapping, so a table added by a migration is backed up and
// restored without the admin code learning its shape.
//
// Generated columns are excluded from both directions: a column the database
// computes cannot be written, and its value is recomputed on restore.

var insertableColumnPredicate = `a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = '' AND a.attidentity <> 'a'`

// The PostgreSQL store is the backend the admin backup and restore commands
// run against. The in-memory store deliberately does not implement
// AdminTableStore, so those commands refuse it.
var _ AdminTableStore = (*PostgresStore)(nil)

// TableColumns returns the table's insertable columns in declaration order.
func (s *PostgresStore) TableColumns(ctx context.Context, table string) ([]AdminColumn, error) {
	query := `SELECT a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod)
	          FROM pg_catalog.pg_attribute a
	          JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
	          JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	          WHERE n.nspname = $1 AND c.relname = $2 AND ` + insertableColumnPredicate + `
	          ORDER BY a.attnum`
	rows, err := s.db.QueryContext(ctx, query, s.schema, table)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns := make([]AdminColumn, 0)
	for rows.Next() {
		var column AdminColumn
		if err := rows.Scan(&column.Name, &column.Type); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("table %q not found in schema %q", table, s.schema)
	}
	return columns, nil
}

// DumpTable returns every row of the table as a JSON array of objects keyed by
// column name.
func (s *PostgresStore) DumpTable(ctx context.Context, table string) ([]byte, error) {
	columns, err := s.TableColumns(ctx, table)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = quoteIdent(column.Name)
	}
	query := fmt.Sprintf(
		`SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json) FROM (SELECT %s FROM %s.%s) t`,
		strings.Join(names, ", "), quoteIdent(s.schema), quoteIdent(table))
	var data []byte
	if err := s.db.QueryRowContext(ctx, query).Scan(&data); err != nil {
		return nil, fmt.Errorf("dump table %q: %w", table, err)
	}
	return data, nil
}

// LoadTable inserts the rows of the JSON array into the table, then advances
// the table's identity sequence so a subsequent insert does not reuse a
// restored id.
func (s *PostgresStore) LoadTable(ctx context.Context, table string, data []byte) error {
	columns, err := s.TableColumns(ctx, table)
	if err != nil {
		return err
	}
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = quoteIdent(column.Name)
	}
	list := strings.Join(names, ", ")
	qualified := quoteIdent(s.schema) + "." + quoteIdent(table)
	// json_populate_recordset maps each object onto the table's own row type, so
	// PostgreSQL performs every type conversion (uuid, jsonb, timestamptz,
	// arrays) exactly as it would for a normal insert. Any key the target no
	// longer has is ignored.
	query := fmt.Sprintf(
		`INSERT INTO %s (%s) SELECT %s FROM json_populate_recordset(NULL::%s, $1::json)`,
		qualified, list, list, qualified)
	if _, err := s.db.ExecContext(ctx, query, string(data)); err != nil {
		return fmt.Errorf("load table %q: %w", table, err)
	}
	return s.syncIdentitySequences(ctx, table)
}

// TruncateTables removes every row of the named tables in one statement.
//
// One statement, not one per table: PostgreSQL rejects a TRUNCATE that names a
// table another table still references unless that referencing table is part of
// the same command, so a well-ordered sequence of single-table statements fails
// anyway. No CASCADE is used, because that would also empty an extension's table
// the caller never named.
func (s *PostgresStore) TruncateTables(ctx context.Context, tables []string) error {
	if len(tables) == 0 {
		return nil
	}
	qualified := make([]string, 0, len(tables))
	for _, table := range tables {
		qualified = append(qualified, quoteIdent(s.schema)+"."+quoteIdent(table))
	}
	if _, err := s.db.ExecContext(ctx, "TRUNCATE TABLE "+strings.Join(qualified, ", ")); err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	return nil
}

// syncIdentitySequences advances each identity sequence of the table past the
// largest restored value. A restore writes ids explicitly, which does not move
// the sequence, so without this a later insert could collide.
func (s *PostgresStore) syncIdentitySequences(ctx context.Context, table string) error {
	query := `SELECT a.attname FROM pg_catalog.pg_attribute a
	          JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
	          JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	          WHERE n.nspname = $1 AND c.relname = $2 AND a.attnum > 0 AND NOT a.attisdropped
	            AND a.attidentity <> ''`
	rows, err := s.db.QueryContext(ctx, query, s.schema, table)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var identityColumns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		identityColumns = append(identityColumns, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(identityColumns) == 0 {
		return nil
	}
	qualified := quoteIdent(s.schema) + "." + quoteIdent(table)
	// pg_get_serial_sequence takes a text regclass; the double quotes inside the
	// literal address exactly this schema and table.
	regclass := "'" + strings.ReplaceAll(qualified, "'", "''") + "'"
	for _, column := range identityColumns {
		statement := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence(%s, $1), COALESCE((SELECT MAX(%s) FROM %s), 1))`,
			regclass, quoteIdent(column), qualified)
		var advanced int64
		if err := s.db.QueryRowContext(ctx, statement, column).Scan(&advanced); err != nil {
			return fmt.Errorf("sync identity sequence of %q: %w", table, err)
		}
	}
	return nil
}
