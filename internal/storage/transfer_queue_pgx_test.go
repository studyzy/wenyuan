//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// openTransferTestStore opens a fresh schema on the real database for one test.
func openTransferTestStore(t *testing.T, prefix string) *PostgresStore {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the transfer store test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	backend := &PostgresBackend{}
	opened, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema, RunMigrations: true})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := opened.(*PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg
}

// A document write that carries the source's creation time keeps it, so an
// imported document shows when it was written rather than when it was copied.
func TestPutDocumentPreservesCreatedAt(t *testing.T) {
	ctx := context.Background()
	store := openTransferTestStore(t, "doc_created")
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	source := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	stored, err := store.PutDocument(ctx, "b1", "d1", Document{ID: "d1", OriginalText: "hello", CreatedAt: source})
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CreatedAt.UTC().Truncate(time.Microsecond).Equal(source) {
		t.Fatalf("created_at = %s, want the source's %s", stored.CreatedAt, source)
	}
	read, err := store.GetDocument(ctx, "b1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if !read.CreatedAt.UTC().Truncate(time.Microsecond).Equal(source) {
		t.Fatalf("read back created_at = %s, want %s", read.CreatedAt, source)
	}
}

// The two maintenance queues round-trip through the transfer API: graph rows by
// unit id, entity rows re-resolved by canonical name.
func TestMaintenanceQueueTransferRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTransferTestStore(t, "maint_transfer")
	if _, err := store.CreateBank(ctx, Bank{BankID: "src", Name: "src"}); err != nil {
		t.Fatal(err)
	}
	unitID := newID()
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: unitID, BankID: "src", Text: "u", FactType: TypeWorld, Metadata: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	enqueued := time.Date(2022, 1, 2, 3, 4, 5, 0, time.UTC)
	if n, err := store.ImportGraphMaintenance(ctx, "src", []MaintenanceQueueRow{{ID: unitID, EnqueuedAt: enqueued}}); err != nil || n != 1 {
		t.Fatalf("ImportGraphMaintenance = %d, %v", n, err)
	}
	graph, err := store.ExportGraphMaintenance(ctx, "src")
	if err != nil {
		t.Fatal(err)
	}
	if len(graph) != 1 || graph[0].ID != unitID {
		t.Fatalf("graph queue = %+v, want the queued unit", graph)
	}

	entity, err := store.ResolveEntity(ctx, "src", "Alice", EntityKindRegular, true)
	if err != nil {
		t.Fatal(err)
	}
	if entity.ID == "" {
		t.Fatal("the entity must be created")
	}
	if n, err := store.ImportEntityMaintenance(ctx, "src", []MaintenanceQueueRow{
		{CanonicalName: "Alice", EnqueuedAt: enqueued},
		{CanonicalName: "Nobody", EnqueuedAt: enqueued},
	}); err != nil || n != 1 {
		t.Fatalf("ImportEntityMaintenance = %d, %v (only the resolvable name lands)", n, err)
	}
	entities, err := store.ExportEntityMaintenance(ctx, "src")
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 || entities[0].CanonicalName != "Alice" {
		t.Fatalf("entity queue = %+v, want Alice", entities)
	}
}
