//go:build pgx

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
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
		// Drop the schema, not just the connection: every run migrates a
		// uniquely named schema, and leaving it behind accumulates until the
		// cross-schema discovery routines run out of lock table opening a
		// cursor per schema.
		if dropper, ok := store.(interface{ DropSchemaForTest(string) error }); ok {
			_ = dropper.DropSchemaForTest(schema)
		}
		if closer, ok := store.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})
	if _, err := store.CreateBank(ctx, Bank{BankID: "read-bank", Name: "read-bank"}); err != nil {
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
	if metadata, ok := doc.Metadata.(map[string]any); !ok || metadata["source"] != "slack" {
		t.Fatalf("metadata was not persisted: %v", doc.Metadata)
	}
	if doc.RetainParams["context"] != "standup" {
		t.Fatalf("retain params were not persisted: %v", doc.RetainParams)
	}

	// One unit for the document, so the derived count is one rather than zero.
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("unit-1"), BankID: "read-bank", Text: "a fact", DocumentID: "doc-1",
		FactType: TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListDocuments(ctx, DocumentQuery{BankID: "read-bank", Limit: 100})
	if err != nil {
		t.Fatalf("list documents: %v", err)
	}
	if page.Total != 1 || len(page.Documents) != 1 {
		t.Fatalf("list returned %d of %d", len(page.Documents), page.Total)
	}
	if page.Documents[0].MemoryUnitCount != 1 {
		t.Fatalf("derived unit count = %d, want 1", page.Documents[0].MemoryUnitCount)
	}

	// An explicit page size of zero selects nothing while the total still
	// counts the matches, which is the page contract both backends share.
	empty, err := store.ListDocuments(ctx, DocumentQuery{BankID: "read-bank", Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 1 || len(empty.Documents) != 0 {
		t.Fatalf("a zero limit must page nothing, got %d of %d", len(empty.Documents), empty.Total)
	}

	// A tag filter narrows to documents carrying every listed tag under the
	// all modes; the listing's default, any_strict, is the reference's OR.
	page, err = store.ListDocuments(ctx, DocumentQuery{
		BankID: "read-bank", Tags: []string{"ops", "weekly"}, TagsMatch: TagsMatchAllStrict, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("tag filter returned %d, want 1", page.Total)
	}
	page, err = store.ListDocuments(ctx, DocumentQuery{
		BankID: "read-bank", Tags: []string{"ops", "absent"}, TagsMatch: TagsMatchAllStrict, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("an all filter requiring an absent tag must match nothing, got %d", page.Total)
	}
	// The default mode is neither: one carried tag is enough.
	page, err = store.ListDocuments(ctx, DocumentQuery{
		BankID: "read-bank", Tags: []string{"ops", "absent"}, TagsMatch: TagsMatchAnyStrict, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("an any_strict filter must match on one carried tag, got %d", page.Total)
	}
}

// test-tag-counts: tags are aggregated with their usage counts.
func TestPostgresTagCounts(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	for i, tags := range [][]string{{"ops", "weekly"}, {"ops"}} {
		// The histogram is sourced from the bank's memory units, the table the
		// reference's tag listing reads, so the fixture carries the tags there
		// rather than on a document.
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: pgUUID(fmt.Sprintf("tagged-unit-%d", i)), BankID: "read-bank",
			Text: "text", FactType: TypeWorld, Tags: tags,
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
			ID: pgUUID(fmt.Sprintf("unit-%d", i)), BankID: "read-bank",
			Text: "fact", FactType: TypeWorld,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertLink(ctx, MemoryLink{
		BankID: "read-bank", FromUnitID: pgUUID("unit-0"), ToUnitID: pgUUID("unit-1"),
		LinkType: LinkCauses, Weight: 1,
	}); err != nil {
		t.Fatal(err)
	}

	graph, err := store.Graph(ctx, GraphQuery{BankID: "read-bank", Limit: 100})
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
	bounded, err := store.Graph(ctx, GraphQuery{BankID: "read-bank", Limit: 1})
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
	page, err := store.ListEntities(ctx, EntityQuery{BankID: "read-bank", Limit: 100})
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

// The unfiltered entity read serves the stored mention_count rather than
// recomputing it: the reference reads the entities row, and that row lists an
// entity even when its count has fallen to zero. The count itself is kept in
// step with the postings — dropping the link gives the mention back, so the
// stored value tracks the live set — while a tag filter is what recomputes,
// over the matching memories only.
func TestPostgresEntityReadServesStoredCountWithoutTagFilter(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	if _, err := store.PutDocument(ctx, "read-bank", "doc-1", Document{OriginalText: "a fact"}); err != nil {
		t.Fatal(err)
	}
	unitID := pgUUID("unit-1")
	if _, err := store.UpsertUnit(ctx, MemoryUnit{
		ID: unitID, BankID: "read-bank", Text: "a fact", DocumentID: "doc-1",
		FactType: TypeWorld, Tags: []string{"ops"},
	}); err != nil {
		t.Fatal(err)
	}
	entity, err := store.ResolveEntity(ctx, "read-bank", "Tuesday", EntityKindRegular, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertUnitEntity(ctx, unitID, entity.ID); err != nil {
		t.Fatal(err)
	}
	// Dropping the link (the shape a re-extraction leaves) gives the mention
	// back, so the stored count lands at zero; the entity row itself stays, so
	// the unfiltered read still lists it, unlike the filter below.
	if err := store.SetUnitEntities(ctx, "read-bank", unitID, nil, false); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListEntities(ctx, EntityQuery{BankID: "read-bank", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Entities[0].MentionCount != 0 {
		t.Fatalf("stored read = %d entities, mention_count %+v", page.Total, page.Entities)
	}

	// The same read under a tag filter recomputes: no live memory mentions the
	// entity, so it is not listed at all.
	page, err = store.ListEntities(ctx, EntityQuery{BankID: "read-bank", Tags: []string{"ops"}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Entities) != 0 {
		t.Fatalf("filtered read = %d of %d, want none", len(page.Entities), page.Total)
	}
}

// The document read counts its units in the same statement, so the count does
// not depend on the document appearing in a listing window.
func TestPostgresGetDocumentCountsUnits(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	if _, err := store.PutDocument(ctx, "read-bank", "doc-1", Document{OriginalText: "text"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"unit-1", "unit-2"} {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: pgUUID(id), BankID: "read-bank", Text: "a fact", DocumentID: "doc-1", FactType: TypeWorld,
		}); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := store.GetDocument(ctx, "read-bank", "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.MemoryUnitCount != 2 {
		t.Fatalf("memory_unit_count = %d, want 2", doc.MemoryUnitCount)
	}
}

// A chunk is resolved by its own identifier, wherever its bank sits in the
// listing: the addressed chunk route carries no bank, so the lookup must not
// depend on a page of the bank listing.
func TestPostgresFindChunkIgnoresTheBankPageWindow(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()

	if _, err := store.CreateBank(ctx, Bank{BankID: "chunk-bank", Name: "chunk-bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDocument(ctx, "chunk-bank", "doc-1", Document{OriginalText: "Alice moved to Berlin."}); err != nil {
		t.Fatal(err)
	}
	ids, err := store.PutDocumentChunks(ctx, "chunk-bank", "doc-1",
		[]DocumentChunk{{ChunkIndex: 0, ChunkText: "Alice moved to Berlin."}}, true)
	if err != nil {
		t.Fatal(err)
	}
	chunkID := ids[0]

	// Banks created after the fixture sort ahead of it, filling a
	// ListBanks(Limit: 500) page of their own.
	for i := range 500 {
		filler := fmt.Sprintf("filler-%03d", i)
		if _, err := store.CreateBank(ctx, Bank{BankID: filler, Name: "Filler"}); err != nil {
			t.Fatal(err)
		}
	}
	banks, err := store.ListBanks(ctx, BankQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(banks.Banks) != 500 {
		t.Fatalf("bank page = %d rows, want the window to be full", len(banks.Banks))
	}
	for _, listed := range banks.Banks {
		if listed.BankID == "chunk-bank" {
			t.Fatal("the fixture bank is still inside the first page of the bank listing")
		}
	}

	chunk, err := store.FindChunk(ctx, chunkID)
	if err != nil {
		t.Fatalf("find chunk %s: %v", chunkID, err)
	}
	if chunk.BankID != "chunk-bank" || chunk.DocumentID != "doc-1" || chunk.ChunkText != "Alice moved to Berlin." {
		t.Fatalf("chunk = %+v, want its own bank, document and text", chunk)
	}
	if _, err := store.FindChunk(ctx, "no-such-chunk"); !errors.Is(err, ErrChunkNotFound) {
		t.Fatalf("unknown chunk error = %v, want ErrChunkNotFound", err)
	}
}

// document_metadata is the document's metadata value as it stands: the value
// written into retain_params.metadata is echoed even when it is not an object,
// a value supplied to the document write is merged into that member, and a
// member that is present but empty reads as no metadata.
func TestPostgresDocumentMetadataFollowsRetainParams(t *testing.T) {
	store := postgresReadStore(t)
	ctx := context.Background()
	cases := []struct {
		name   string
		doc    Document
		want   any
		member any
	}{
		{"object", Document{RetainParams: map[string]any{"metadata": map[string]any{"source": "slack"}}},
			map[string]any{"source": "slack"}, map[string]any{"source": "slack"}},
		{"string", Document{RetainParams: map[string]any{"metadata": "slack"}}, "slack", "slack"},
		{"array", Document{RetainParams: map[string]any{"metadata": []any{"a", "b"}}}, []any{"a", "b"}, []any{"a", "b"}},
		{"merged", Document{Metadata: map[string]any{"source": "slack"}},
			map[string]any{"source": "slack"}, map[string]any{"source": "slack"}},
		{"empty object", Document{RetainParams: map[string]any{"metadata": map[string]any{}}}, nil, map[string]any{}},
		{"empty array", Document{RetainParams: map[string]any{"metadata": []any{}}}, nil, []any{}},
		{"absent", Document{RetainParams: map[string]any{"context": "notes"}}, nil, nil},
	}
	for i, tc := range cases {
		docID := fmt.Sprintf("doc-metadata-%d", i)
		doc := tc.doc
		doc.OriginalText = "body"
		if _, err := store.PutDocument(ctx, "read-bank", docID, doc); err != nil {
			t.Fatalf("%s: put document: %v", tc.name, err)
		}
		stored, err := store.GetDocument(ctx, "read-bank", docID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored.Metadata, tc.want) {
			t.Fatalf("%s: metadata = %#v, want %#v", tc.name, stored.Metadata, tc.want)
		}
		if !reflect.DeepEqual(stored.RetainParams["metadata"], tc.member) {
			t.Fatalf("%s: retain_params member = %#v, want %#v", tc.name, stored.RetainParams["metadata"], tc.member)
		}
		page, err := store.ListDocuments(ctx, DocumentQuery{BankID: "read-bank", Query: docID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Documents) != 1 {
			t.Fatalf("%s: listed %d documents, want 1", tc.name, len(page.Documents))
		}
		if !reflect.DeepEqual(page.Documents[0].Metadata, tc.want) {
			t.Fatalf("%s: listed metadata = %#v, want %#v", tc.name, page.Documents[0].Metadata, tc.want)
		}
	}
}
