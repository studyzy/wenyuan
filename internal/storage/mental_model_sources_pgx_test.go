//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// The two existence checks a mental model's refresh and staleness hang on run
// SQL over memory_units: "is there anything in my scope to read" and "has
// anything in my scope been written since I last looked". Both name the fact
// type filter, and the column is fact_type - the reference's schema gives the
// fact type its own name and does not call it `type`. Naming the wrong column
// made the query fail, and because both checks answer with a bool that a failure
// reports as "nothing", a fact-typed scope silently never refreshed: a knowledge
// page (whose built-in trigger is `fact_types: ["observation"]`) stayed empty
// forever. These tests pin the filter against real SQL.

// scopeSourceStore opens a migrated store in its own schema and registers its
// teardown.
func scopeSourceStore(t *testing.T) (*PostgresStore, string) {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the scope source test was not run")
	}
	schema := fmt.Sprintf("scope_%d", time.Now().UnixNano())
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
	return pg, schema
}

// insertUnit writes one memory_units row of the given fact type and returns its
// mention time.
func insertUnit(t *testing.T, pg *PostgresStore, bankID, factType, text string) time.Time {
	t.Helper()
	mentioned := time.Now().UTC()
	query := fmt.Sprintf(`INSERT INTO %s.memory_units (id, bank_id, text, fact_type, mentioned_at, tags)
	           VALUES (gen_random_uuid(), $1, $2, $3, $4, '{}')`, quoteIdent(pg.schema))
	if _, err := pg.db.ExecContext(context.Background(), query, bankID, text, factType, mentioned); err != nil {
		t.Fatalf("insert a %s unit: %v", factType, err)
	}
	return mentioned
}

func TestScopeHasSourcesFiltersByFactType(t *testing.T) {
	pg, _ := scopeSourceStore(t)
	insertUnit(t, pg, "b1", "world", "Alice lives in Berlin")
	model := MentalModel{
		ID: "m1", BankID: "b1", SourceQuery: "where does alice live",
		Trigger: map[string]any{"fact_types": []any{"observation"}},
	}
	if pg.ScopeHasSources(context.Background(), "b1", model, time.Time{}) {
		t.Error("a scope naming observations must not be sourced by a world fact")
	}
	insertUnit(t, pg, "b1", "observation", "Alice is settled in Berlin")
	if !pg.ScopeHasSources(context.Background(), "b1", model, time.Time{}) {
		t.Error("a scope naming observations must be sourced by the observation that exists")
	}
	// The window is part of the check, and it is the caller's delta bound: a
	// watermark after the write leaves nothing in scope.
	if pg.ScopeHasSources(context.Background(), "b1", model, time.Now().UTC()) {
		t.Error("a window opening after every write must find no source")
	}
}

func TestScopeStalenessFiltersByFactType(t *testing.T) {
	pg, _ := scopeSourceStore(t)
	written := insertUnit(t, pg, "b2", "world", "Alice lives in Berlin")
	model := MentalModel{
		ID: "m2", BankID: "b2", SourceQuery: "where does alice live",
		Trigger: map[string]any{"fact_types": []any{"observation"}},
	}
	// A world fact written after the watermark cannot make an observation-scoped
	// model stale, and a column error would have read as "not stale" either way -
	// which is why the observation is written afterwards and must flip it.
	if !pg.IsStale(context.Background(), "b2", model) {
		t.Error("a model with no watermark is stale by definition")
	}
	stamped := model
	stamped.LastMemorySeenAt = &written
	if pg.IsStale(context.Background(), "b2", stamped) {
		t.Error("a world write must not make an observation-scoped model stale")
	}
	insertUnit(t, pg, "b2", "observation", "Alice is settled in Berlin")
	if !pg.IsStale(context.Background(), "b2", stamped) {
		t.Error("an in-scope observation written after the watermark must make the model stale")
	}
}
