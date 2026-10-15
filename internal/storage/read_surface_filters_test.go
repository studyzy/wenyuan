package storage

import (
	"context"
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
