//go:build pgx

package storage

import (
	"context"
	"testing"
)

// test-postgres-invalid-vector-index: an index left invalid by an interrupted
// concurrent build is reported as absent, and the reconcile drops it before it
// rebuilds — CREATE ... IF NOT EXISTS alone would accept the invalid row as the
// index asked for and never rebuild it.
//
// Whether the catalogue read and the drop-then-create order are right can only
// be proven against a real PostgreSQL, which is why this carries the pgx build
// tag; it is skipped without WENYUAN_TEST_DATABASE_URL.
func TestPostgresInvalidVectorIndexIsRebuilt(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	pg := store.(*PostgresStore)
	bank := "vec-bank"
	name := BankVectorIndexName("world", bank)
	qualified := quoteIdent(pg.schema)

	// A partition holding a row, so the plan wants an index for it.
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.memory_units (bank_id, text, fact_type) VALUES ($1, 'x', 'world')`, bank); err != nil {
		t.Fatalf("insert memory unit: %v", err)
	}
	ddl := BankVectorIndexDDL(pg.schema, pg.vectorExtension, bank, BankVectorIndexAction{
		FactType: "world", IndexName: name, Create: true,
	})
	if _, err := pg.db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("create index: %v", err)
	}
	// Simulate the cancelled or failed build: the catalogue row is present but
	// not valid. Direct catalogue surgery needs superuser rights, which the CI
	// database grants; a restricted one is skipped rather than failed.
	if _, err := pg.db.ExecContext(ctx,
		`UPDATE pg_index SET indisvalid = false WHERE indexrelid = $1::regclass`, qualified+"."+quoteIdent(name)); err != nil {
		t.Skipf("cannot mark the index invalid (needs a superuser test role): %v", err)
	}

	state := vectorState(t, pg, bank, "world")
	if state.Present || !state.Invalid {
		t.Fatalf("an invalid index must read as absent: %+v", state)
	}

	actions, err := pg.ReconcileBankVectorIndexes(ctx, bank, 1)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(actions) != 2 || actions[0].Create || !actions[1].Create {
		t.Fatalf("plan = %+v, want a drop of the invalid index then a rebuild", actions)
	}
	rebuilt := vectorState(t, pg, bank, "world")
	if !rebuilt.Present || rebuilt.Invalid {
		t.Fatalf("the rebuilt index must read as present and usable: %+v", rebuilt)
	}
}

// test-postgres-delete-drops-vector-index: deleting a bank drops its per-bank
// vector indexes, because after the row is gone no reconcile can name them.
func TestPostgresDeleteBankDropsVectorIndexes(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	pg := store.(*PostgresStore)
	bank := "vec-bank"
	name := BankVectorIndexName("world", bank)

	ddl := BankVectorIndexDDL(pg.schema, pg.vectorExtension, bank, BankVectorIndexAction{
		FactType: "world", IndexName: name, Create: true,
	})
	if _, err := pg.db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("create index: %v", err)
	}
	if _, err := pg.DeleteBank(ctx, bank); err != nil {
		t.Fatalf("delete bank: %v", err)
	}
	var found int
	if err := pg.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = $1 AND c.relname = $2`, pg.schema, name).Scan(&found); err != nil {
		t.Fatalf("count index: %v", err)
	}
	if found != 0 {
		t.Fatalf("the bank's vector index survived its deletion")
	}
}

// test-postgres-sweep-sees-below-threshold-holders: a bank that still carries
// one of our indexes is listed by the sweep even when no partition reaches the
// threshold, so the drop branch is reachable for a bank that shrank.
func TestPostgresSweepSeesBelowThresholdIndexHolders(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	pg := store.(*PostgresStore)

	// A second bank with neither rows nor an index, to prove the list is not
	// simply every bank.
	if _, err := pg.CreateBank(ctx, Bank{BankID: "quiet-bank", Name: "quiet"}); err != nil {
		t.Fatalf("create second bank: %v", err)
	}
	name := BankVectorIndexName("world", "vec-bank")
	ddl := BankVectorIndexDDL(pg.schema, pg.vectorExtension, "vec-bank", BankVectorIndexAction{
		FactType: "world", IndexName: name, Create: true,
	})
	if _, err := pg.db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("create index: %v", err)
	}

	// No partition of either bank reaches the threshold, so only the
	// index-holding bank is discovered — by the catalogue, not by a count.
	banks, err := pg.BanksNeedingVectorIndexes(ctx, 1000)
	if err != nil {
		t.Fatalf("discover banks: %v", err)
	}
	found := false
	for _, bankID := range banks {
		if bankID == "quiet-bank" {
			t.Fatalf("a bank with no index and no qualifying partition must not be listed: %v", banks)
		}
		if bankID == "vec-bank" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a bank still holding one of our indexes must be listed: %v", banks)
	}

	// Reconcile it: with no rows, the plan drops the now-unwanted index.
	actions, err := pg.ReconcileBankVectorIndexes(ctx, "vec-bank", 1000)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(actions) != 1 || actions[0].Create {
		t.Fatalf("plan = %+v, want a single drop", actions)
	}
}

// vectorState returns one fact-type partition's state for the bank, failing the
// test when the fact type is absent.
func vectorState(t *testing.T, pg *PostgresStore, bankID, factType string) BankVectorIndexState {
	t.Helper()
	states, err := pg.BankVectorIndexStates(context.Background(), bankID)
	if err != nil {
		t.Fatalf("states: %v", err)
	}
	for _, state := range states {
		if state.FactType == factType {
			return state
		}
	}
	t.Fatalf("no state for fact type %q", factType)
	return BankVectorIndexState{}
}
