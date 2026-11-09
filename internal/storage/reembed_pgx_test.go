//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// countingEmbedder records how many times it was asked to embed and returns a
// deterministic unit vector, so a reindex can be shown to produce the width the
// column demands without a model.
type countingEmbedder struct {
	calls int
	texts int
	fail  error
}

func (e *countingEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return e.vector(), nil
}

func (e *countingEmbedder) EmbedPassages(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	e.texts += len(texts)
	if e.fail != nil {
		return nil, e.fail
	}
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = e.vector()
	}
	return vectors, nil
}

func (e *countingEmbedder) vector() []float32 {
	vector := make([]float32, EmbeddingDimensions)
	vector[0] = 1
	return vector
}

// openBackfillStore opens a fresh schema on the test database, or skips.
func openBackfillStore(t *testing.T) *PostgresStore {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the reindex test was not run")
	}
	schema := fmt.Sprintf("reembed_%d", time.Now().UnixNano())
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

// insertUnembeddedUnit writes a unit through the store (which embeds what it
// writes) and then clears its retrieval columns, standing in for a row an
// import landed without them.
func insertUnembeddedUnit(t *testing.T, pg *PostgresStore, bankID, id, text string) {
	t.Helper()
	ctx := context.Background()
	if err := pg.EnsureBank(ctx, bankID); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	if _, err := pg.UpsertUnit(ctx, MemoryUnit{BankID: bankID, Text: text, FactType: "world"}); err != nil {
		t.Fatalf("write unit: %v", err)
	}
	// The row just written is the only one in the bank.
	schema := pg.Schema()
	_, err := pg.db.ExecContext(ctx, `UPDATE `+quoteIdent(schema)+`.memory_units
	                                     SET id = $1::uuid, embedding = NULL, search_vector = NULL
	                                   WHERE bank_id = $2`, id, bankID)
	if err != nil {
		t.Fatalf("blank retrieval columns: %v", err)
	}
}

// TestBackfillVectorsFillsMissingRetrievalColumns is the repair an archive
// import needs: a row that arrived without a vector becomes rankable again.
func TestBackfillVectorsFillsMissingRetrievalColumns(t *testing.T) {
	pg := openBackfillStore(t)
	insertUnembeddedUnit(t, pg, "acme", "11111111-1111-1111-1111-111111111111", "kotlin coroutines")

	embedder := &countingEmbedder{}
	report, err := pg.BackfillVectors(context.Background(), "acme", embedder)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if report.RowsRebuilt != 1 || embedder.texts != 1 {
		t.Fatalf("report = %+v, embedded texts = %d; want one row and one text", report, embedder.texts)
	}

	var hasEmbedding, hasVector bool
	if err := pg.db.QueryRowContext(context.Background(),
		`SELECT embedding IS NOT NULL, search_vector IS NOT NULL AND search_vector != ''::tsvector
		   FROM `+quoteIdent(pg.Schema())+`.memory_units WHERE bank_id = 'acme'`).
		Scan(&hasEmbedding, &hasVector); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !hasEmbedding || !hasVector {
		t.Fatalf("embedding written = %v, search_vector written = %v; want both", hasEmbedding, hasVector)
	}

	// A completed pass leaves nothing to do, which is what makes a second run
	// after an interruption safe.
	again := &countingEmbedder{}
	second, err := pg.BackfillVectors(context.Background(), "acme", again)
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if second.RowsRebuilt != 0 || again.calls != 0 {
		t.Fatalf("second run rebuilt %d rows in %d calls; want a no-op", second.RowsRebuilt, again.calls)
	}
}

// TestBackfillVectorsRefusesWithoutEmbeddings pins that a run with no model is
// refused rather than half-done: rebuilding only the search_vector would leave
// the row unrankable by the semantic arm while reporting success.
func TestBackfillVectorsRefusesWithoutEmbeddings(t *testing.T) {
	pg := openBackfillStore(t)
	insertUnembeddedUnit(t, pg, "acme", "22222222-2222-2222-2222-222222222222", "golang")
	if _, err := pg.BackfillVectors(context.Background(), "acme", nil); err == nil {
		t.Fatal("backfill without an embedding generator returned no error")
	}
}
