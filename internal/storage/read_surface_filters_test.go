package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// seedRetained stores one item as a document plus its extracted facts.
func seedRetained(t *testing.T, store *MemoryStore, content, docID string, tags []string) {
	t.Helper()
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {{Text: content}},
	}}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: docID, Tags: tags}},
		Extraction: extractor,
	}); err != nil {
		t.Fatal(err)
	}
}

// doc-q-0001: `q` matches the document id, not the document body.
func TestMemoryListDocumentsQueryMatchesID(t *testing.T) {
	store := NewMemoryStore("public")
	seedRetained(t, store, "body holds the needle word", "plain-doc", nil)
	seedRetained(t, store, "unrelated body", "needle-doc", nil)

	page, err := store.ListDocuments(context.TODO(), DocumentQuery{BankID: "b1", Query: "needle", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Documents) != 1 || page.Documents[0].ID != "needle-doc" {
		t.Fatalf("q must filter the document id: %+v", page)
	}
}

// doc-tags-0001: an omitted tags_match defaults to any_strict (overlap),
// matching a document that carries only one of the requested tags.
func TestMemoryListDocumentsDefaultTagsMatchAnyStrict(t *testing.T) {
	store := NewMemoryStore("public")
	seedRetained(t, store, "one", "doc-x", []string{"x"})
	seedRetained(t, store, "two", "doc-xy", []string{"x", "y"})
	seedRetained(t, store, "three", "doc-z", []string{"z"})

	page, err := store.ListDocuments(context.TODO(), DocumentQuery{
		BankID: "b1", Tags: []string{"x", "y"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 {
		t.Fatalf("default any_strict must overlap (2 docs), got %d: %+v", page.Total, page)
	}
	all, err := store.ListDocuments(context.TODO(), DocumentQuery{
		BankID: "b1", Tags: []string{"x", "y"}, TagsMatch: TagsMatchAll, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 1 {
		t.Fatalf("all must require both tags, got %d", all.Total)
	}
}

// tags-src-0001: the tag histogram is sourced from memory units, so a tag only
// a unit carries still appears.
func TestMemoryListTagsFromUnits(t *testing.T) {
	store := NewMemoryStore("public")
	store.mu.Lock()
	store.units["u1"] = MemoryUnit{ID: "u1", BankID: "b1", Tags: []string{"unit-only"}}
	store.mu.Unlock()

	tags, err := store.ListTags(context.TODO(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Tag != "unit-only" || tags[0].Count != 1 {
		t.Fatalf("tags must come from memory units: %+v", tags)
	}
}

// ops-scope-0001: exclude_parents drops parent batch operations and the single
// operation read is scoped to the bank.
func TestMemoryOperationsExcludeParentsAndBankScope(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.TODO()
	parent := Operation{OperationID: "p1", BankID: "b1", OperationType: OperationTypeRetain,
		Status: StatusPending, ResultMetadata: map[string]any{"is_parent": true}}
	child := Operation{OperationID: "c1", BankID: "b1", OperationType: OperationTypeRetain,
		Status: StatusPending, ResultMetadata: map[string]any{"parent_operation_id": "p1", "sub_batch_index": 0}}
	other := Operation{OperationID: "p2", BankID: "b2", OperationType: OperationTypeRetain,
		Status: StatusPending, ResultMetadata: map[string]any{"is_parent": true}}
	for _, op := range []Operation{parent, child, other} {
		if _, err := store.PutOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}

	page, err := store.ListOperations(ctx, OperationQuery{BankID: "b1", ExcludeParents: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Operations) != 1 || page.Operations[0].OperationID != "c1" {
		t.Fatalf("exclude_parents page = %+v", page)
	}

	if _, err := store.GetOperation(ctx, "b2", "p1"); err != ErrOperationNotFound {
		t.Fatalf("cross-bank read = %v, want ErrOperationNotFound", err)
	}
	got, err := store.GetOperation(ctx, "b1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsParent {
		t.Fatalf("parent flag not derived: %+v", got)
	}
	children, err := store.GetOperationChildren(ctx, "b1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].OperationID != "c1" {
		t.Fatalf("children = %+v", children)
	}
}

// curation-0001: invalidating moves a unit out of the live view into the
// archive, and reverting restores it.
func TestMemoryInvalidateAndRevert(t *testing.T) {
	store := NewMemoryStore("public")
	store.mu.Lock()
	store.units["m1"] = MemoryUnit{ID: "m1", BankID: "b1", Text: "live"}
	store.mu.Unlock()
	ctx := context.TODO()

	invalidated, reason := "invalidated", "wrong fact"
	unit, err := store.UpdateMemory(ctx, "b1", "m1", MemoryPatch{State: &invalidated, Reason: &reason})
	if err != nil {
		t.Fatal(err)
	}
	if unit.InvalidatedAt == nil || unit.InvalidationReason == nil || *unit.InvalidationReason != reason {
		t.Fatalf("invalidation not recorded: %+v", unit)
	}
	live, err := store.ListMemories(ctx, MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if live.Total != 0 {
		t.Fatalf("live view must exclude invalidated units: %+v", live)
	}
	arch, err := store.ListMemories(ctx, MemoryQuery{BankID: "b1", State: "invalidated", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if arch.Total != 1 || arch.Memories[0].ID != "m1" {
		t.Fatalf("archive view = %+v", arch)
	}

	valid := "valid"
	if _, err := store.UpdateMemory(ctx, "b1", "m1", MemoryPatch{State: &valid}); err != nil {
		t.Fatal(err)
	}
	restored, err := store.ListMemories(ctx, MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if restored.Total != 1 || restored.Memories[0].InvalidatedAt != nil {
		t.Fatalf("revert must restore the live view: %+v", restored)
	}
}

// A chunk identifier is the chunks table's key, so a chunk is resolved by that
// identifier alone, wherever its bank sits in the listing: the addressed chunk
// route has no bank in its path. The lookup must therefore not depend on a page
// of the bank listing, or a chunk in a bank past that page is unreachable.
func TestMemoryFindChunkIgnoresTheBankPageWindow(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	seedRetained(t, store, "Alice moved to Berlin.", "doc-1", nil)
	page, err := store.ListChunks(ctx, ChunkQuery{BankID: "b1", DocumentID: "doc-1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) == 0 {
		t.Fatal("the retained document has no chunks")
	}
	chunkID := page.Chunks[0].ChunkID

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
		if listed.BankID == "b1" {
			t.Fatal("the fixture bank is still inside the first page of the bank listing")
		}
	}

	chunk, err := store.FindChunk(ctx, chunkID)
	if err != nil {
		t.Fatalf("find chunk %s: %v", chunkID, err)
	}
	if chunk.BankID != "b1" || chunk.DocumentID != "doc-1" {
		t.Fatalf("chunk = %+v, want its own bank and document", chunk)
	}
	if _, err := store.FindChunk(ctx, "no-such-chunk"); !errors.Is(err, ErrChunkNotFound) {
		t.Fatalf("unknown chunk error = %v, want ErrChunkNotFound", err)
	}
}

// document_metadata is the document's metadata value as it stands. The
// reference keeps it only inside retain_params.metadata and serves that member
// through a truth test, so a value that is present is echoed even when it is
// not an object, and one that is present but empty reads as no metadata.
func TestMemoryDocumentMetadataFollowsRetainParams(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	cases := []struct {
		name   string
		retain map[string]any
		want   any
	}{
		{"object", map[string]any{"metadata": map[string]any{"source": "slack"}}, map[string]any{"source": "slack"}},
		{"string", map[string]any{"metadata": "slack"}, "slack"},
		{"array", map[string]any{"metadata": []any{"a", "b"}}, []any{"a", "b"}},
		{"number", map[string]any{"metadata": float64(3)}, float64(3)},
		{"empty object", map[string]any{"metadata": map[string]any{}}, nil},
		{"empty array", map[string]any{"metadata": []any{}}, nil},
		{"null", map[string]any{"metadata": nil}, nil},
		{"absent", map[string]any{"context": "notes"}, nil},
		{"supplied", nil, "slack"},
	}
	for i, tc := range cases {
		docID := fmt.Sprintf("doc-%d", i)
		doc := Document{OriginalText: "body", RetainParams: tc.retain}
		if tc.retain == nil {
			// A caller may supply the metadata value itself instead of the
			// retain parameters it is kept in; the write merges it there.
			doc.Metadata = tc.want
		}
		if _, err := store.PutDocument(ctx, "b1", docID, doc); err != nil {
			t.Fatal(err)
		}
		if tc.retain == nil {
			stored, err := store.GetDocument(ctx, "b1", docID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored.RetainParams["metadata"], tc.want) {
				t.Fatalf("%s: retain_params member = %#v, want %#v", tc.name, stored.RetainParams["metadata"], tc.want)
			}
		}
		doc, err := store.GetDocument(ctx, "b1", docID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(doc.Metadata, tc.want) {
			t.Fatalf("%s: detail metadata = %#v, want %#v", tc.name, doc.Metadata, tc.want)
		}
		list, err := store.ListDocuments(ctx, DocumentQuery{BankID: "b1", Query: docID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Documents) != 1 {
			t.Fatalf("%s: listed %d documents, want 1", tc.name, len(list.Documents))
		}
		if !reflect.DeepEqual(list.Documents[0].Metadata, tc.want) {
			t.Fatalf("%s: listed metadata = %#v, want %#v", tc.name, list.Documents[0].Metadata, tc.want)
		}
	}
}
