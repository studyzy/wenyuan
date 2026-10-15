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
