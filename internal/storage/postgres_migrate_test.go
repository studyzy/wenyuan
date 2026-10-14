//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// The migration ledger is what makes a restart safe: every process applies the
// series, and only the migrations a schema has not recorded run. Re-applying a
// recorded one would re-create its objects, so the second pass has to be a
// no-op rather than merely tolerated.
func TestMigrateIsRepeatable(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the migration repeatability test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("migrate_repeat_%d", time.Now().UnixNano())
	store, err := (&PostgresBackend{}).Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	defer func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	}()

	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	before, err := pg.appliedMigrations(ctx)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	after, err := pg.appliedMigrations(ctx)
	if err != nil {
		t.Fatalf("read the ledger after the second pass: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("the second pass changed the ledger: %d -> %d entries", len(before), len(after))
	}
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(series) {
		t.Fatalf("the ledger records %d migrations, the series holds %d", len(after), len(series))
	}
}
