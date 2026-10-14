//go:build pgx

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// postgresParityApp composes the served application over a real PostgreSQL
// store whose schema was built by this project's migration series. The test is
// skipped when no database URL is configured, so the default gate stays
// offline; it must be RUN explicitly rather than assumed.
//
// The schema name is unique per run and dropped afterwards: the database is
// shared with other work, and a retired schema that keeps its tables would
// accumulate.
func postgresParityApp(t *testing.T) *App {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the PostgreSQL parity test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("parity_e2e_%d", time.Now().UnixNano())
	backend := &storage.PostgresBackend{}
	store, err := backend.Open(ctx, storage.BackendConfig{
		DatabaseURL: url, Schema: schema, RunMigrations: true,
	})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pg, ok := store.(*storage.PostgresStore)
	if !ok {
		t.Fatalf("expected a postgres store, got %T", store)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
		_ = cleanupCtx
	})

	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	cfg.EnableBankConfigAPI = true
	application, err := New(ctx, cfg, Dependencies{Store: store})
	if err != nil {
		t.Fatalf("compose application: %v", err)
	}
	return application
}

// test-postgres-parity-retain-recall-reflect: the whole served surface works
// against a database built by this project's migrations, which is the shape a
// reference deployment already has. The routes exercised are the ones a client
// depends on to write a memory and read it back, so a column rename that broke
// one of them would surface here.
func TestPostgresParityRetainRecallReflect(t *testing.T) {
	a := postgresParityApp(t)
	const bank = "parity-bank"

	// The bank is created through the served route, so the disposition and
	// mission travel the same path a client uses.
	rec := exchange(a, http.MethodPut, "/v1/default/banks/"+bank,
		`{"name":"Parity Bank","disposition":{"skepticism":5,"literalism":4,"empathy":5},"mission":"be precise"}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank = %d, body %s", rec.Code, rec.Body.String())
	}
	created := decodeJSON(t, rec)
	if created["bank_id"] != bank {
		t.Fatalf("created bank_id = %v", created["bank_id"])
	}

	// The config read route renders the disposition back in the flat four-key
	// form, which is derived from the single disposition object.
	rec = exchange(a, http.MethodGet, "/v1/default/banks/"+bank+"/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read config = %d, body %s", rec.Code, rec.Body.String())
	}
	cfgBody := decodeJSON(t, rec)
	overrides, ok := cfgBody["overrides"].(map[string]any)
	if !ok {
		t.Fatalf("config overrides = %#v", cfgBody["overrides"])
	}
	if value, _ := overrides["disposition_skepticism"].(float64); value != 5 {
		t.Fatalf("disposition_skepticism = %v, want 5", overrides["disposition_skepticism"])
	}
	if overrides["reflect_mission"] != "be precise" {
		t.Fatalf("reflect_mission = %v, want be precise", overrides["reflect_mission"])
	}

	// A retain write through the served route, then a recall that reads it back.
	rec = exchange(a, http.MethodPost, "/v1/default/banks/"+bank+"/memories",
		`{"items":[{"content":"Alice works at Google as a data scientist.","document_id":"doc-parity"}]}`)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("retain = %d, body %s", rec.Code, rec.Body.String())
	}

	// The read routes the console depends on.
	for _, path := range []string{
		"/v1/default/banks/" + bank + "/documents",
		"/v1/default/banks/" + bank + "/entities",
		"/v1/default/banks/" + bank + "/tags",
		"/v1/default/banks/" + bank + "/memories/list",
		"/v1/default/banks/" + bank + "/graph",
		"/v1/default/banks/" + bank + "/entities/graph",
	} {
		rec = exchange(a, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, body %s", path, rec.Code, rec.Body.String())
		}
	}

	// The bank list is the query that joins banks to aliases and orders by the
	// write watermark, so it touches the renamed bank columns.
	rec = exchange(a, http.MethodGet, "/v1/default/banks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list banks = %d, body %s", rec.Code, rec.Body.String())
	}
	var listBody struct {
		Banks []map[string]any `json:"banks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("list banks is not JSON: %v", err)
	}
	if len(listBody.Banks) == 0 {
		t.Fatal("the created bank is missing from the list")
	}

	// A recall finds the memory that was just written.
	//
	// The retain above stored a document and its chunks; with no model provider
	// configured there is no extraction, so no memory unit was derived. The
	// fact is written directly here to exercise the recall path against the
	// renamed unit columns, which is what an extracted fact would produce.
	if _, err := a.Store.UpsertUnit(context.Background(), storage.MemoryUnit{
		BankID: bank, Text: "Alice works at Google as a data scientist", FactType: storage.TypeWorld,
	}); err != nil {
		t.Fatalf("write a fact: %v", err)
	}
	rec = exchange(a, http.MethodPost, "/v1/default/banks/"+bank+"/memories/recall",
		`{"query":"Google"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall = %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Google") {
		t.Fatalf("recall did not surface the stored memory: %s", rec.Body.String())
	}

	// The stats route aggregates over the renamed fact-type column.
	rec = exchange(a, http.MethodGet, "/v1/default/banks/"+bank+"/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats = %d, body %s", rec.Code, rec.Body.String())
	}

	// Deleting the bank exercises the cascading deletes the reference declares.
	rec = exchange(a, http.MethodDelete, "/v1/default/banks/"+bank, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete bank = %d, body %s", rec.Code, rec.Body.String())
	}
}
