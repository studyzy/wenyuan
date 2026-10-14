//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// The maintenance discovery routines are created by the migration series and
// are callable on a real database.
//
// This is the end-to-end proof that the migration runner can carry a routine at
// all: the routine bodies are dollar-quoted and full of semicolons, so a
// splitter that divided the body would fail the CREATE rather than the call.
func TestMaintenanceRoutinesExist(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the routine test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("maint_routines_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema, RunMigrations: true})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	defer func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	}()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Each routine must exist and be callable with its declared arity.
	//
	// The database is shared, so the assertions count rows belonging to this
	// test's own schema rather than the whole database: a routine that walks
	// pg_class correctly reports every other schema too.
	calls := []struct {
		name  string
		query string
		args  []any
	}{
		{"schemas_with_expired_rows", `SELECT count(*) FROM ` + quoteIdent(schema) + `.schemas_with_expired_rows($1, $2, $3) WHERE schemas_with_expired_rows = $4`, []any{"audit_log", "started_at", 30, schema}},
		{"banks_needing_consolidation", `SELECT count(*) FROM ` + quoteIdent(schema) + `.banks_needing_consolidation() WHERE schema_name = $1`, []any{schema}},
		{"schemas_with_expired_operations", `SELECT count(*) FROM ` + quoteIdent(schema) + `.schemas_with_expired_operations($1) WHERE schemas_with_expired_operations = $2`, []any{30, schema}},
		{"mental_models_with_cron", `SELECT count(*) FROM ` + quoteIdent(schema) + `.mental_models_with_cron() WHERE schema_name = $1`, []any{schema}},
	}
	for _, call := range calls {
		var found int
		if err := pg.db.QueryRowContext(ctx, call.query, call.args...).Scan(&found); err != nil {
			t.Errorf("%s is not callable: %v", call.name, err)
			continue
		}
		// A schema the migration just created holds no expired rows and no
		// scheduled model, so each routine must report nothing about it.
		if found != 0 {
			t.Errorf("%s reported %d row(s) for this fresh schema, want none", call.name, found)
		}
	}

	// A non-positive window is "keep forever", so the routine must report
	// nothing rather than treating zero as "delete everything". Asked about
	// this schema specifically, since other schemas may hold expired work.
	var kept int
	if err := pg.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+quoteIdent(schema)+`.schemas_with_expired_operations($1) WHERE schemas_with_expired_operations = $2`,
		0, schema).Scan(&kept); err != nil {
		t.Fatalf("zero-day sweep: %v", err)
	}
	if kept != 0 {
		t.Errorf("a zero retention window reported this schema, want none")
	}
}

// The two mental-model columns the scheduled refresh needs exist after the
// migration, and the backfill copies the old refresh time into the new
// watermark so a previously refreshed model is not treated as never seen.
func TestMentalModelMaintenanceColumns(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the column test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("mm_columns_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema, RunMigrations: true})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	defer func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	}()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, column := range []string{"last_memory_seen_at", "last_refresh_failed_at"} {
		var present bool
		if err := pg.db.QueryRowContext(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM information_schema.columns
			  WHERE table_schema = $1 AND table_name = 'mental_models' AND column_name = $2
			)`, schema, column).Scan(&present); err != nil {
			t.Fatalf("probe %s: %v", column, err)
		}
		if !present {
			t.Errorf("mental_models.%s was not created", column)
		}
	}
}

// The retention routine reports a schema only when that schema genuinely holds
// an expired row, so it is a probe rather than a constant.
//
// The empty case above cannot distinguish "walked pg_class and found nothing"
// from "returned a constant empty set", which is exactly the failure a broken
// routine would present as. This seeds one expired row and one fresh row.
func TestRetentionRoutineReportsOnlyExpiredSchemas(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the retention probe was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("maint_retention_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	defer func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	}()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	reporting := func(days int) []string {
		t.Helper()
		rows, err := pg.db.QueryContext(ctx,
			`SELECT schemas_with_expired_rows($1, $2, $3)`, "audit_log", "started_at", days)
		if err != nil {
			t.Fatalf("call the routine: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var found []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			if name == schema {
				found = append(found, name)
			}
		}
		return found
	}

	// A fresh row is not expired, so this schema must not be reported.
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(schema)+`.audit_log (id, action, transport, bank_id, started_at) VALUES (gen_random_uuid(), 'a', 'http', 'b1', now())`); err != nil {
		t.Fatalf("insert fresh row: %v", err)
	}
	if got := reporting(30); len(got) != 0 {
		t.Errorf("a fresh row made the schema look expired: %v", got)
	}

	// An old row is expired, so now it must be.
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(schema)+`.audit_log (id, action, transport, bank_id, started_at) VALUES (gen_random_uuid(), 'a', 'http', 'b1', now() - interval '60 days')`); err != nil {
		t.Fatalf("insert expired row: %v", err)
	}
	if got := reporting(30); len(got) != 1 {
		t.Errorf("an expired row was not reported: %v", got)
	}
}
