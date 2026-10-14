//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// postgresMigrationStore opens a real backend in its own schema, without
// running migrations, so the test controls when they happen.
func postgresMigrationStore(t *testing.T) *PostgresStore {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the migration version test was not run")
	}
	ctx := context.Background()
	backend := &PostgresBackend{}
	schema := fmt.Sprintf("migver_e2e_%d", time.Now().UnixNano())
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatal("expected a postgres store")
	}
	// Drop the schema as well as closing the pool: a run that leaves its schema
	// behind contributes to an accumulation the cross-schema routines then have
	// to walk.
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	return pg
}

// test-migrations-are-recorded-and-not-replayed: an applied migration is
// recorded, and a second run applies nothing.
//
// Replaying the series on every start is both wasted work and a hazard, because
// a migration is only safe to repeat while someone remembers to write it that
// way.
func TestMigrationsAreRecordedAndNotReplayed(t *testing.T) {
	store := postgresMigrationStore(t)
	ctx := context.Background()

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.appliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(series) {
		t.Fatalf("recorded %d migrations, want all %d", len(first), len(series))
	}

	// A second run must be a no-op rather than an error or a replay.
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("second migrate must be a no-op, got %v", err)
	}
	second, err := store.appliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != len(first) {
		t.Fatalf("a second run changed the record from %d to %d", len(first), len(second))
	}
}

// test-a-migration-is-atomic: a migration whose statements fail leaves nothing
// recorded, so it is retried rather than silently skipped.
func TestFailedMigrationIsNotRecorded(t *testing.T) {
	store := postgresMigrationStore(t)
	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// A deliberately broken migration must not be recorded.
	broken := Migration{Version: 9999, Name: "broken", SQL: "CREATE TABLE this_is_not_valid_sql (;"}
	if err := store.applyMigration(ctx, broken); err == nil {
		t.Fatal("a malformed migration must fail")
	}
	applied, err := store.appliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if applied[9999] {
		t.Fatal("a failed migration must not be recorded as applied")
	}
}
