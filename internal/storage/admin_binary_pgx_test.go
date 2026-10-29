//go:build pgx

package storage

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

// The binary restore is the path that loads a whole-database archive back:
// raw PostgreSQL binary COPY streams, replayed inside one transaction. These
// tests drive it against a real database on a table the migration series
// actually created, so the stream shape is the server's own, not hand-built.
// observation_history is the fixture because it stands alone (no foreign keys,
// so truncating it needs no company) and carries an identity column, which
// exercises the sequence re-sync.

// openMigratedStore opens a throwaway schema with the full migration series.
func openMigratedStore(t *testing.T, prefix string) *PostgresStore {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the binary restore test was not run")
	}
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	store, err := (&PostgresBackend{}).Open(context.Background(), BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg
}

// captureBinaryStream copies a whole table out in PostgreSQL binary COPY
// format, the same shape a backup archive carries.
func captureBinaryStream(t *testing.T, pg *PostgresStore, table string, columns []AdminColumn) []byte {
	t.Helper()
	raw, ok := pg.db.(interface {
		RawConn(context.Context) (*sql.Conn, error)
	})
	if !ok {
		t.Fatal("the driver does not expose a raw connection")
	}
	conn, err := raw.RawConn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	var buffer bytes.Buffer
	err = conn.Raw(func(driverConn any) error {
		stdlibConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			t.Fatalf("pooled connection is %T, want the pgx driver", driverConn)
		}
		qualified := quoteIdent(pg.defaultSchema) + "." + quoteIdent(table)
		names := make([]string, 0, len(columns))
		for _, column := range columns {
			names = append(names, quoteIdent(column.Name))
		}
		statement := "COPY " + qualified + " (" + strings.Join(names, ", ") + ") TO STDOUT WITH (FORMAT BINARY)"
		_, err := stdlibConn.Conn().PgConn().CopyTo(context.Background(), &buffer, statement)
		return err
	})
	if err != nil {
		t.Fatalf("copy %s out: %v", table, err)
	}
	return buffer.Bytes()
}

// TestRestoreBinaryRoundTrip replays a captured stream back through
// RestoreBinary: rows the archive does not carry must be gone, rows it does
// carry must be back exactly, and the identity sequence must have moved past
// the restored ids.
func TestRestoreBinaryRoundTrip(t *testing.T) {
	pg := openMigratedStore(t, "binrest")
	ctx := context.Background()
	table := quoteIdent(pg.defaultSchema) + ".observation_history"

	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+table+` (observation_id, bank_id, content)
		 VALUES (gen_random_uuid(), 'b1', '{"v":1}'::jsonb), (gen_random_uuid(), 'b1', '{"v":2}'::jsonb)`); err != nil {
		t.Fatalf("seed observation history: %v", err)
	}
	columns, err := pg.TableColumns(ctx, "observation_history")
	if err != nil {
		t.Fatal(err)
	}
	load := BinaryLoad{Table: "observation_history", Columns: columnNames(columns), Data: captureBinaryStream(t, pg, "observation_history", columns)}

	// A row written after the stream was captured: the restore replaces the
	// table, so it must not survive.
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+table+` (observation_id, bank_id, content)
		 VALUES (gen_random_uuid(), 'b1', '{"v":"unarchived"}'::jsonb)`); err != nil {
		t.Fatalf("seed unarchived row: %v", err)
	}

	if err := pg.RestoreBinary(ctx, []BinaryLoad{load}); err != nil {
		t.Fatalf("restore binary: %v", err)
	}

	var restored int
	if err := pg.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+table+` WHERE content->>'v' IN ('1','2')`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != 2 {
		t.Fatalf("restored observation_history rows = %d, want 2", restored)
	}
	var unarchived int
	if err := pg.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+table+` WHERE content->>'v' = 'unarchived'`).Scan(&unarchived); err != nil {
		t.Fatal(err)
	}
	if unarchived != 0 {
		t.Fatal("a row written after the archive was taken must not survive the restore")
	}

	// The identity sequence must sit past the restored ids, so the next insert
	// neither collides nor fails.
	var nextID int
	if err := pg.db.QueryRowContext(ctx,
		`INSERT INTO `+table+` (observation_id, bank_id, content)
		 VALUES (gen_random_uuid(), 'b1', '{"v":3}'::jsonb) RETURNING id`).Scan(&nextID); err != nil {
		t.Fatalf("insert after restore: %v", err)
	}
	if nextID <= 2 {
		t.Fatalf("the identity sequence did not advance past the restored rows; new id = %d", nextID)
	}
}

// TestRestoreBinaryFailureLeavesDataInPlace pins the atomicity: a stream the
// server rejects must roll the whole restore back, leaving the previous rows
// untouched rather than a truncated table.
func TestRestoreBinaryFailureLeavesDataInPlace(t *testing.T) {
	pg := openMigratedStore(t, "binfail")
	ctx := context.Background()
	table := quoteIdent(pg.defaultSchema) + ".observation_history"

	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+table+` (observation_id, bank_id, content)
		 VALUES (gen_random_uuid(), 'b1', '{"v":1}'::jsonb)`); err != nil {
		t.Fatalf("seed observation history: %v", err)
	}
	columns, err := pg.TableColumns(ctx, "observation_history")
	if err != nil {
		t.Fatal(err)
	}
	stream := captureBinaryStream(t, pg, "observation_history", columns)
	// Corrupt the stream by cutting it short: the signature stays valid, the
	// server rejects the truncated tuple mid-COPY.
	broken := stream[:len(stream)-4]

	if err := pg.RestoreBinary(ctx, []BinaryLoad{{Table: "observation_history", Columns: columnNames(columns), Data: broken}}); err == nil {
		t.Fatal("a truncated stream must fail the restore")
	}
	var rows int
	if err := pg.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("observation_history rows = %d after a failed restore, want the previous 1", rows)
	}
}

func columnNames(columns []AdminColumn) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.Name)
	}
	return names
}
