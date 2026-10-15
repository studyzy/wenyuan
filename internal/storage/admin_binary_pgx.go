//go:build pgx

package storage

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// RestoreBinary implements the binary-COPY half of AdminTableStore. The whole
// restore runs on one pooled connection inside one transaction, so either
// every table lands or none does, and the raw bytes of each stream are handed
// to PostgreSQL untouched: the server decodes every value with the target
// column's own binary receive function, so this code never needs to know a
// column's type.
func (s *PostgresStore) RestoreBinary(ctx context.Context, loads []BinaryLoad) error {
	if len(loads) == 0 {
		return nil
	}
	raw, ok := s.db.(interface {
		RawConn(context.Context) (*sql.Conn, error)
	})
	if !ok {
		return fmt.Errorf("binary restore needs a driver that hands over a raw connection")
	}
	conn, err := raw.RawConn(ctx)
	if err != nil {
		return fmt.Errorf("acquire a connection for the restore: %w", err)
	}
	defer func() { _ = conn.Close() }()
	return conn.Raw(func(driverConn any) error {
		stdlibConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("binary restore needs the pgx driver; the pooled connection is %T", driverConn)
		}
		return restoreBinaryOnConn(ctx, s.schema, stdlibConn.Conn(), loads)
	})
}

// DumpBinary captures the named tables as binary COPY streams on one pooled
// connection inside one REPEATABLE READ transaction, so the set is a
// consistent snapshot no matter how long the copy takes. The columns each
// stream was taken against travel with it, read from the same snapshot.
func (s *PostgresStore) DumpBinary(ctx context.Context, tables []string) ([]BinaryDump, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	raw, ok := s.db.(interface {
		RawConn(context.Context) (*sql.Conn, error)
	})
	if !ok {
		return nil, fmt.Errorf("binary backup needs a driver that hands over a raw connection")
	}
	conn, err := raw.RawConn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire a connection for the backup: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var dumps []BinaryDump
	err = conn.Raw(func(driverConn any) error {
		stdlibConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("binary backup needs the pgx driver; the pooled connection is %T", driverConn)
		}
		dumps, err = dumpBinaryOnConn(ctx, s.schema, stdlibConn.Conn(), tables)
		return err
	})
	if err != nil {
		return nil, err
	}
	return dumps, nil
}

// dumpBinaryOnConn copies every table out on one connection inside one
// REPEATABLE READ transaction. The stream is the server's own binary COPY
// output, byte-for-byte; no type here needs to be known to this code.
func dumpBinaryOnConn(ctx context.Context, schema string, conn *pgx.Conn, tables []string) ([]BinaryDump, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin the backup snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
		return nil, fmt.Errorf("isolate the backup snapshot: %w", err)
	}

	dumps := make([]BinaryDump, 0, len(tables))
	for _, table := range tables {
		columns, err := tableColumnsOnTx(ctx, tx, schema, table)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(columns))
		for _, column := range columns {
			names = append(names, quoteIdent(column.Name))
		}
		copySQL := fmt.Sprintf("COPY %s (%s) TO STDOUT WITH (FORMAT BINARY)",
			quoteIdent(schema)+"."+quoteIdent(table), strings.Join(names, ", "))
		var buffer bytes.Buffer
		// The connection is inside the snapshot transaction the tx handle
		// opened, so every table is read from the same database state.
		if _, err := conn.PgConn().CopyTo(ctx, &buffer, copySQL); err != nil {
			return nil, fmt.Errorf("dump table %q: %w", table, err)
		}
		dumps = append(dumps, BinaryDump{Table: table, Columns: columns, Data: buffer.Bytes()})
	}
	return dumps, tx.Commit(ctx)
}

// tableColumnsOnTx reads the table's insertable columns in declaration order
// on the backup's own transaction, mirroring TableColumns on the pool.
func tableColumnsOnTx(ctx context.Context, tx pgx.Tx, schema, table string) ([]AdminColumn, error) {
	query := `SELECT a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod)
	          FROM pg_catalog.pg_attribute a
	          JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
	          JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	          WHERE n.nspname = $1 AND c.relname = $2 AND ` + insertableColumnPredicate + `
	          ORDER BY a.attnum`
	rows, err := tx.Query(ctx, query, schema, table)
	if err != nil {
		return nil, fmt.Errorf("list columns of %q: %w", table, err)
	}
	defer rows.Close()
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
		return nil, fmt.Errorf("table %q not found in schema %q", table, schema)
	}
	return columns, nil
}

// restoreBinaryOnConn runs the restore on one pgx connection: a single
// transaction truncates the full table set in one statement (PostgreSQL
// refuses to truncate a referenced table unless the referencing tables clear
// in the same command), replays every stream in the order given so references
// exist by the time a row lands, and finally advances identity sequences.
func restoreBinaryOnConn(ctx context.Context, schema string, conn *pgx.Conn, loads []BinaryLoad) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin the restore transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tables := make([]string, 0, len(loads))
	for _, load := range loads {
		tables = append(tables, load.Table)
	}
	truncate := make([]string, 0, len(tables))
	for _, table := range tables {
		truncate = append(truncate, quoteIdent(schema)+"."+quoteIdent(table))
	}
	statement := "TRUNCATE TABLE " + strings.Join(truncate, ", ")
	if _, err := tx.Exec(ctx, statement); err != nil {
		return fmt.Errorf("truncate tables: %w", err)
	}
	for _, load := range loads {
		columns := make([]string, 0, len(load.Columns))
		for _, column := range load.Columns {
			columns = append(columns, quoteIdent(column))
		}
		copySQL := fmt.Sprintf("COPY %s (%s) FROM STDIN WITH (FORMAT BINARY)",
			quoteIdent(schema)+"."+quoteIdent(load.Table), strings.Join(columns, ", "))
		// The connection is inside the transaction the tx handle opened, so
		// the COPY lands (or rolls back) with everything else.
		if _, err := conn.PgConn().CopyFrom(ctx, bytes.NewReader(load.Data), copySQL); err != nil {
			return fmt.Errorf("restore table %q: %w", load.Table, err)
		}
	}
	for _, table := range tables {
		if err := syncIdentitySequencesOnTx(ctx, tx, schema, table); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// syncIdentitySequencesOnTx advances each identity column's sequence past the
// largest value just restored — the transactional twin of syncIdentitySequences
// on the pooled path.
func syncIdentitySequencesOnTx(ctx context.Context, tx pgx.Tx, schema, table string) error {
	rows, err := tx.Query(ctx, identityColumnsQuery, schema, table)
	if err != nil {
		return fmt.Errorf("list identity columns of %q: %w", table, err)
	}
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if len(columns) == 0 {
		return nil
	}
	qualified := quoteIdent(schema) + "." + quoteIdent(table)
	// pg_get_serial_sequence takes a text regclass; the double quotes inside
	// the literal address exactly this schema and table.
	regclass := "'" + strings.ReplaceAll(qualified, "'", "''") + "'"
	for _, column := range columns {
		statement := fmt.Sprintf(
			`SELECT setval(pg_get_serial_sequence(%s, $1), COALESCE((SELECT MAX(%s) FROM %s), 1))`,
			regclass, quoteIdent(column), qualified)
		if _, err := tx.Exec(ctx, statement, column); err != nil {
			return fmt.Errorf("sync identity sequence of %q: %w", table, err)
		}
	}
	return nil
}
