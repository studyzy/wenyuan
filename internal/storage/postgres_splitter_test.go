//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// A migration carrying a PL/pgSQL routine actually creates and runs it on a
// real database.
//
// The splitter's unit tests assert the text is divided correctly; this asserts
// the consequence, because "the body was handed over whole" is only true if the
// database accepts it and the routine returns the value its body computed. A
// split inside the body fails the CREATE, which is the failure mode the
// maintenance routines would hit.
func TestRealDatabaseCreatesDollarQuotedRoutine(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the routine-creation test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("splitter_probe_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	defer func() { _ = pg.Close() }()
	if _, err := pg.db.ExecContext(ctx, `CREATE SCHEMA `+quoteIdent(schema)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = pg.db.ExecContext(ctx, `DROP SCHEMA `+quoteIdent(schema)+` CASCADE`) }()

	// The body deliberately contains several semicolons and a nested block, so
	// a split on the first inner one would produce an unusable statement.
	migration := Migration{Version: 9999, Name: "probe", SQL: `
CREATE OR REPLACE FUNCTION ` + quoteIdent(schema) + `.probe_answer() RETURNS int
LANGUAGE plpgsql
AS $$
DECLARE
  n int;
BEGIN
  n := 40;
  n := n + 2;
  RETURN n;
END;
$$;

CREATE OR REPLACE FUNCTION ` + quoteIdent(schema) + `.probe_constant() RETURNS int
LANGUAGE sql
AS $$
  SELECT 1;
$$;
`}
	statements := migration.Statements()
	if len(statements) != 2 {
		t.Fatalf("split into %d statements, want the two routines: %#v", len(statements), statements)
	}
	for _, statement := range statements {
		if _, err := pg.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("applying a statement failed: %v", err)
		}
	}
	var answer int
	if err := pg.db.QueryRowContext(ctx, `SELECT `+quoteIdent(schema)+`.probe_answer()`).Scan(&answer); err != nil {
		t.Fatalf("call the routine: %v", err)
	}
	if answer != 42 {
		t.Fatalf("routine returned %d, want 42 — the body was not created whole", answer)
	}
}
