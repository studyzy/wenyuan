//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The vector width is a deployment setting the reference resolves from the
// configured embedding model at startup, not a property of the schema it dumps.
// The generated series therefore carries whichever width the reference
// deployment had, and this service reconciles the column at startup - so a
// regeneration of the series cannot silently change the stored width, and a
// database whose vectors came from another model is reported rather than
// written into with the wrong width.

// TestMigrateResizesEmbeddingColumnsToDeclaredWidth drives the reconcile on a
// fresh schema: the series declares the reference deployment's width and the
// migration must leave the columns at the width the provider boundary declares.
func TestMigrateResizesEmbeddingColumnsToDeclaredWidth(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the embedding dimension test was not run")
	}
	schema := fmt.Sprintf("embdim_%d", time.Now().UnixNano())
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
	for _, table := range embeddingTables {
		width, err := embeddingColumnWidth(context.Background(), pg.db, schema, table)
		if err != nil {
			t.Fatalf("read %s.embedding width: %v", table, err)
		}
		if width != EmbeddingDimensions {
			t.Errorf("%s.embedding width = %d, want the declared %d", table, width, EmbeddingDimensions)
		}
	}
}

// TestMigrateRefusesEmbeddingDimensionChangeWithStoredVectors pins the refusal:
// a database holding vectors of another width must be reported, never
// converted, because the conversion would invalidate every stored vector.
func TestMigrateRefusesEmbeddingDimensionChangeWithStoredVectors(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the embedding dimension test was not run")
	}
	schema := fmt.Sprintf("embdim_%d", time.Now().UnixNano())
	store, err := (&PostgresBackend{}).Open(context.Background(), BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Stand in for a database written by a deployment with another embedding
	// model: a wider column holding one vector of that width.
	foreign := EmbeddingDimensions + 128
	qualified := quoteIdent(schema) + "." + quoteIdent("memory_units")
	if _, err := pg.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN embedding TYPE vector(%d)", qualified, foreign)); err != nil {
		t.Fatalf("widen the column: %v", err)
	}
	vec := strings.TrimSuffix(strings.Repeat("0.5,", foreign), ",")
	if _, err := pg.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (id, bank_id, text, embedding) VALUES (gen_random_uuid(), gen_random_uuid(), 'foreign', '[%s]'::vector)", qualified, vec)); err != nil {
		t.Fatalf("insert a foreign vector: %v", err)
	}
	err = pg.Migrate(ctx)
	if err == nil {
		t.Fatal("migrate must refuse a database holding vectors of another width")
	}
	for _, want := range []string{"memory_units", fmt.Sprint(foreign), fmt.Sprint(EmbeddingDimensions), "1 embedded rows"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
}

// TestMigrateLeavesGlobalMemoryUnitsVectorIndexDropped pins the replay
// distinction on an adopted database that still carries a global vector index
// on memory_units: upstream deliberately keeps none there, because per-bank
// backends serve every vector search from the per-bank partial indexes (#5707)
// and its repair migration e8c1a4f7b2d9 drops any stale global one by catalogue
// lookup. The resize must drop it and leave it dropped, while a per-bank
// partial index and mental_models' global index still come back.
func TestMigrateLeavesGlobalMemoryUnitsVectorIndexDropped(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the embedding dimension test was not run")
	}
	schema := fmt.Sprintf("embdim_%d", time.Now().UnixNano())
	store, err := (&PostgresBackend{}).Open(context.Background(), BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Stand in for the adopted database: empty embedding columns widened by
	// another embedding model, so the reconcile alters rather than refuses,
	// plus the three index shapes such a database can carry. The stale global
	// index deliberately does not use the name the reference's older repair
	// dropped by: matching it is a catalogue lookup, not a name match.
	foreign := EmbeddingDimensions + 128
	qualified := func(table string) string { return quoteIdent(schema) + "." + quoteIdent(table) }
	for _, table := range embeddingTables {
		if _, err := pg.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN embedding TYPE vector(%d)", qualified(table), foreign)); err != nil {
			t.Fatalf("widen %s.embedding: %v", table, err)
		}
	}
	exec := func(query string) {
		if _, err := pg.db.ExecContext(ctx, query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(fmt.Sprintf("CREATE INDEX embdim_stale_global ON %s USING hnsw (embedding vector_cosine_ops)", qualified("memory_units")))
	exec(fmt.Sprintf("CREATE INDEX embdim_partial_bank ON %s USING hnsw (embedding vector_cosine_ops) WHERE bank_id = 'adopted-bank'", qualified("memory_units")))
	exec(fmt.Sprintf("CREATE INDEX embdim_mental_models_global ON %s USING hnsw (embedding vector_cosine_ops)", qualified("mental_models")))
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("resize migrate: %v", err)
	}
	for _, table := range embeddingTables {
		width, err := embeddingColumnWidth(ctx, pg.db, schema, table)
		if err != nil {
			t.Fatalf("read %s.embedding width: %v", table, err)
		}
		if width != EmbeddingDimensions {
			t.Errorf("%s.embedding width = %d, want the declared %d", table, width, EmbeddingDimensions)
		}
	}
	indexes := func(table string) map[string]capturedIndex {
		defs, err := embeddingIndexDefinitions(ctx, pg.db, schema, table)
		if err != nil {
			t.Fatalf("list %s embedding indexes: %v", table, err)
		}
		out := make(map[string]capturedIndex, len(defs))
		for _, def := range defs {
			out[def.name] = def
		}
		return out
	}
	memoryUnits := indexes("memory_units")
	if _, ok := memoryUnits["embdim_stale_global"]; ok {
		t.Error("the stale global vector index on memory_units was replayed after the resize; upstream leaves it dropped")
	}
	partial, ok := memoryUnits["embdim_partial_bank"]
	if !ok {
		t.Error("the per-bank partial index did not replay after the resize")
	} else {
		if !partial.partial || partial.amname != "hnsw" {
			t.Errorf("captured per-bank index = partial:%v amname:%q, want partial:true amname:hnsw", partial.partial, partial.amname)
		}
		if !strings.Contains(partial.definition, "WHERE") {
			t.Errorf("replayed per-bank index definition lost its WHERE clause: %s", partial.definition)
		}
	}
	if _, ok := indexes("mental_models")["embdim_mental_models_global"]; !ok {
		t.Error("the mental_models global vector index did not replay after the resize; upstream keeps it")
	}
}
