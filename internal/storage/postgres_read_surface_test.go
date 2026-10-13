//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// postgresReadStore opens a real PostgreSQL backend in its own schema.
func postgresReadStore(t *testing.T) Store {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the PostgreSQL read-surface test was not run")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("read_e2e_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema, RunMigrations: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		if closer, ok := store.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})
	if _, err := store.CreateBank(ctx, Bank{ID: "read-bank", Name: "read-bank"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	return store
}

// test-document-list-carries-metadata: the listing returns the tags, metadata
// and retain parameters that were written, and derives the unit count, rather
// than dropping the columns the table already holds.
func TestPostgresDocumentListCarriesMetadata(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	doc, err := store.PutDocument(ctx, "read-bank", "doc-1", Document{
		OriginalText: "the deployment runs on Tuesdays",
		Tags:         []string{"ops", "weekly"},
		Metadata:     map[string]any{"source": "slack"},
		RetainParams: map[string]any{"context": "standup"},
	})
	if err != nil {
		t.Fatalf("put document: %v", err)
	}
	if len(doc.Tags) != 2 {
		t.Fatalf("stored tags = %v, want two", doc.Tags)
	}
	if doc.Metadata["source"] != "slack" {
		t.Fatalf("metadata was not persisted: %v", doc.Metadata)
	}
	if doc.RetainParams["context"] != "standup" {
		t.Fatalf("retain params were not persisted: %v", doc.RetainParams)
	}

	// One unit for the document, so the derived count is one rather than zero.
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: "unit-1", BankID: "read-bank", Text: "a fact", DocumentID: "doc-1",
		Type: TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListDocuments(ctx, DocumentQuery{BankID: "read-bank"})
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if page.Total != 1 || len(page.Documents) != 1 {
		t.Fatalf("list returned %d of %d", len(page.Documents), page.Total)
	}
	if page.Documents[0].MemoryUnitCount != 1 {
		t.Fatalf("derived unit count = %d, want 1", page.Documents[0].MemoryUnitCount)
	}

	// A tag filter narrows to documents carrying every listed tag.
	page, err = store.ListDocuments(ctx, DocumentQuery{BankID: "read-bank", Tags: []string{"ops", "weekly"}})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("tag filter returned %d, want 1", page.Total)
	}
	page, err = store.ListDocuments(ctx, DocumentQuery{BankID: "read-bank", Tags: []string{"ops", "absent"}})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("a tag filter requiring an absent tag must match nothing, got %d", page.Total)
	}
}

// test-tag-counts: tags are aggregated with their usage counts.
func TestPostgresTagCounts(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	for i, tags := range [][]string{{"ops", "weekly"}, {"ops"}} {
		if _, err := store.PutDocument(ctx, "read-bank", fmt.Sprintf("doc-%d", i), Document{
			OriginalText: "text", Tags: tags,
		}); err != nil {
			t.Fatal(err)
		}
	}
	counts, err := store.ListTags(ctx, "read-bank")
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	if len(counts) != 2 {
		t.Fatalf("tag count = %d, want 2: %v", len(counts), counts)
	}
	if counts[0].Tag != "ops" || counts[0].Count != 2 {
		t.Fatalf("the most used tag must come first: %v", counts)
	}
}

// test-graph-bounds-and-keeps-endpoints: the memory graph returns rows and
// nodes, and never an edge whose endpoint fell outside the bound.
func TestPostgresGraphKeepsEndpoints(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: fmt.Sprintf("unit-%d", i), BankID: "read-bank",
			Text: "fact", Type: TypeWorld,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertLink(ctx, MemoryLink{
		ID: "link-1", BankID: "read-bank", FromID: "unit-0", ToID: "unit-1",
		LinkType: LinkCauses,
	}); err != nil {
		t.Fatal(err)
	}

	graph, err := store.Graph(ctx, "read-bank", 100)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if graph.Total != 3 || len(graph.Nodes) != 3 {
		t.Fatalf("graph returned %d nodes of %d", len(graph.Nodes), graph.Total)
	}
	if len(graph.TableRows) != 3 {
		t.Fatalf("graph returned %d rows, want 3", len(graph.TableRows))
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("graph returned %d edges, want 1", len(graph.Edges))
	}

	// With a bound of one node, the edge's other endpoint falls outside the set
	// and the edge must be dropped rather than returned dangling.
	bounded, err := store.Graph(ctx, "read-bank", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(bounded.Nodes) != 1 {
		t.Fatalf("bounded graph returned %d nodes, want 1", len(bounded.Nodes))
	}
	for _, edge := range bounded.Edges {
		kept := false
		for _, node := range bounded.Nodes {
			if node.ID == edge.From || node.ID == edge.To {
				kept = true
			}
		}
		if !kept {
			t.Fatalf("bounded graph returned an edge with no kept endpoint: %+v", edge)
		}
	}
}

// test-entity-listing-and-not-found-chunk: the entity page reports a total, and
// an unknown chunk is a distinct not-found rather than an empty value.
func TestPostgresEntityListingAndChunkNotFound(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	if _, err := store.ResolveEntity(ctx, "read-bank", "Tuesday", EntityKindRegular, true); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListEntities(ctx, EntityQuery{BankID: "read-bank"})
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	if page.Total != 1 || len(page.Entities) != 1 {
		t.Fatalf("entity page = %d of %d", len(page.Entities), page.Total)
	}

	if _, err := store.GetChunk(ctx, "read-bank", "absent"); err != ErrChunkNotFound {
		t.Fatalf("unknown chunk = %v, want ErrChunkNotFound", err)
	}
}
