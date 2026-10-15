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
