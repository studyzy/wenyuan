//go:build pgx

package archiveimport

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// postgresMigrationURL returns the database URL for the migration end-to-end
// test. The test is skipped when it is not configured, so the default gate
// stays offline; it must be RUN explicitly rather than assumed.
func postgresMigrationURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the PostgreSQL end-to-end test was not run")
	}
	return url
}

// test-migrate-postgres-chunk-indexed-facts: a chunk-indexed archive migrates
// into a real PostgreSQL target. memory_units.chunk_id is a foreign key into
// chunks, and the migration writes no chunk rows of its own, so a unit carrying
// the archive's chunk reference is refused by the database. That refusal is the
// failure this test exists to catch, and the memory backend cannot produce it.
func TestMigrateChunkIndexedFactsPostgres(t *testing.T) {
	url := postgresMigrationURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("migration_e2e_%d", time.Now().UnixNano())

	backend := &storage.PostgresBackend{}
	opened, err := backend.Open(ctx, storage.BackendConfig{
		DatabaseURL: url,
		Schema:      schema,
	})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	// Drop the schema and close the pool from one registered cleanup, in that
	// order. A deferred Close would run before any cleanup, so the drop would
	// execute against a closed pool and leave the schema behind.
	t.Cleanup(func() {
		if dropper, ok := opened.(interface{ DropSchemaForTest(string) error }); ok {
			_ = dropper.DropSchemaForTest(schema)
		}
		if closer, ok := opened.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})

	store := opened
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}

	archive := fixtureArchive(t)
	report, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Replace})
	if err != nil {
		t.Fatalf("migration into postgres: %v", err)
	}
	if report.BankID != "migrated-bank" || report.Documents != 2 || report.Facts != 3 {
		t.Fatalf("report = %+v", report)
	}

	// The migrated rows are really in the database, not merely reported.
	stats, err := store.BankStats(ctx, report.BankID)
	if err != nil {
		t.Fatalf("bank stats: %v", err)
	}
	if stats.TotalNodes != 3 || stats.TotalDocuments != 2 {
		t.Fatalf("bank stats = %+v, want 3 nodes and 2 documents", stats)
	}

	// Entity co-occurrences are derived rows the archive does not carry, so the
	// migration rebuilds them from the facts it writes: the entity graph reads
	// that table, and an empty one is a bank whose relations view draws nothing.
	graph, err := store.EntityGraph(ctx, storage.EntityGraphQuery{BankID: report.BankID, Limit: 10, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("entity graph edges = %d, want the one pair the migrated fact witnesses", len(graph.Edges))
	}
	if graph.Edges[0].Weight != 1 {
		t.Fatalf("edge weight = %d, want 1", graph.Edges[0].Weight)
	}
	// The pair's stamp is the witnessing unit's own time, not the write time, so
	// an imported corpus keeps its timeline instead of collapsing onto the
	// migration moment.
	want := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if graph.Edges[0].LastCooccurred == nil || !graph.Edges[0].LastCooccurred.UTC().Equal(want) {
		t.Fatalf("edge last-co-occurred = %v, want the fact's own event time %s",
			graph.Edges[0].LastCooccurred, want)
	}

	// The document's chunks are replayed as rows, and the migrated fact is
	// addressed by the chunk it came from: memory_units.chunk_id is a foreign
	// key into chunks, so a reference to a row the migration never wrote fails
	// the insert on PostgreSQL.
	chunks, err := store.ListChunks(ctx, storage.ChunkQuery{BankID: report.BankID, Limit: 10})
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if chunks.Total != 2 {
		t.Fatalf("chunks stored = %d, want the archive's two", chunks.Total)
	}
	for _, chunk := range chunks.Chunks {
		wantText := archive.Documents[0].Chunks[0].ChunkText
		if chunk.DocumentID == "doc-2" {
			wantText = archive.Documents[1].Chunks[0].ChunkText
		}
		if chunk.ChunkText != wantText {
			t.Fatalf("chunk %s text = %q, want %q", chunk.ChunkID, chunk.ChunkText, wantText)
		}
	}

	memories, err := store.ListMemories(ctx, storage.MemoryQuery{BankID: report.BankID, Limit: 10})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	linked := 0
	for _, unit := range memories.Memories {
		if unit.ChunkID == "" {
			continue
		}
		linked++
		if _, err := store.GetChunk(ctx, report.BankID, unit.ChunkID); err != nil {
			t.Fatalf("unit %s names chunk %s, which must exist: %v", unit.ID, unit.ChunkID, err)
		}
	}
	if linked != 1 {
		t.Fatalf("units carrying a chunk reference = %d, want the one chunk-indexed fact", linked)
	}
}
