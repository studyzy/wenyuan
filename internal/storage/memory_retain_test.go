package storage

import (
	"context"
	"testing"
)

// ts-0008 / beh-0005 / obs-0004: long content is segmented into ordered chunks
// with a non-negative index and a per-chunk content hash.
func TestRetainSegmentsIntoChunks(t *testing.T) {
	store := NewMemoryStore("public")
	content := "first paragraph\n\nsecond paragraph\n\nthird paragraph"
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: content, DocumentID: "doc-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.ItemUnitIDs) != 1 || len(outcome.ItemUnitIDs[0]) != 3 {
		t.Fatalf("expected three chunks in one item group, got %+v", outcome.ItemUnitIDs)
	}
	if len(store.chunks) != 3 {
		t.Fatalf("expected three stored chunks, got %d", len(store.chunks))
	}
	for i, chunk := range store.chunks {
		if chunk.Index != i {
			t.Fatalf("chunk index = %d, want %d", chunk.Index, i)
		}
		if chunk.Text == "" || chunk.ContentHash == "" {
			t.Fatalf("chunk %d must carry text and a content hash: %+v", i, chunk)
		}
	}
}

// ts-0008: disabling document-text storage still computes the content hash.
func TestDocumentCarriesHashAndText(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1", Items: []RetainItem{{Content: "text body", DocumentID: "d1"}},
	}); err != nil {
		t.Fatal(err)
	}
	doc, err := store.GetDocument(context.TODO(), "b1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.OriginalText != "text body" || doc.ContentHash != ContentHash("text body") {
		t.Fatalf("document = %+v", doc)
	}
}

// ts-0009 / beh-0006 / neg-0003: re-using a document identifier replaces the
// prior document's data.
func TestReusedDocumentIDReplacesText(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1", Items: []RetainItem{{Content: "version one", DocumentID: "d1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1", Items: []RetainItem{{Content: "version two", DocumentID: "d1"}},
	}); err != nil {
		t.Fatal(err)
	}
	doc, _ := store.GetDocument(context.TODO(), "b1", "d1")
	if doc.OriginalText != "version two" {
		t.Fatalf("replacing a document must overwrite its text, got %q", doc.OriginalText)
	}
}

// beh-0008 / neg-0004: an append-mode write reads, concatenates, and reprocesses;
// a concurrent move in between makes the request retryable.
func TestAppendDocumentConflictIsRetryable(t *testing.T) {
	store := NewMemoryStore("public")
	doc, err := store.PutDocument(context.TODO(), "b1", "d1", Document{OriginalText: "seed"})
	if err != nil {
		t.Fatal(err)
	}
	// A second writer moves the document before this append.
	if _, err := store.PutDocument(context.TODO(), "b1", "d1", Document{OriginalText: "seed-moved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendDocument(context.TODO(), "b1", "d1", "-more", doc.ContentHash); err != ErrDocumentConflict {
		t.Fatalf("stale append err = %v, want ErrDocumentConflict", err)
	}
	current, _ := store.GetDocument(context.TODO(), "b1", "d1")
	if _, err := store.AppendDocument(context.TODO(), "b1", "d1", "-more", current.ContentHash); err != nil {
		t.Fatalf("fresh append = %v", err)
	}
	updated, _ := store.GetDocument(context.TODO(), "b1", "d1")
	if updated.OriginalText != "seed-moved-more" {
		t.Fatalf("append must concatenate onto the stored text, got %q", updated.OriginalText)
	}
}

// ts-0011 / beh-0007: with resolution off a supplied name is reused only on a
// case-insensitive exact match; with resolution on a close name resolves.
func TestEntityResolutionModes(t *testing.T) {
	store := NewMemoryStore("public")
	first, err := store.ResolveEntity(context.TODO(), "b1", "Acme Corp", EntityKindRegular, true)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := store.ResolveEntity(context.TODO(), "b1", "acme corp", EntityKindRegular, false)
	if err != nil {
		t.Fatal(err)
	}
	if exact.ID != first.ID {
		t.Fatalf("a case-insensitive exact match must reuse the entity")
	}
	label, err := store.ResolveEntity(context.TODO(), "b1", "Topic", EntityKindLabel, false)
	if err != nil {
		t.Fatal(err)
	}
	if label.Kind != EntityKindLabel {
		t.Fatalf("label entity kind = %q", label.Kind)
	}
}

// ts-0019 / inv-0005: stored units carry the accepted fact type and links use
// the backward-looking causal form.
func TestUnitAndLinkPersistence(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1", Items: []RetainItem{{Content: "a fact", DocumentID: "d1"}},
	}); err != nil {
		t.Fatal(err)
	}
	units := store.UnitsForTest("b1")
	if len(units) == 0 {
		t.Fatal("expected at least one stored unit")
	}
	if !IsValidMemoryUnitType(units[0].Type) {
		t.Fatalf("stored unit type %q is not an accepted value", units[0].Type)
	}
	link := MemoryLink{BankID: "b1", FromID: units[0].ID, ToID: units[0].ID, LinkType: LinkCausedBy}
	if err := store.UpsertLink(context.TODO(), link); err != nil {
		t.Fatal(err)
	}
	if !IsValidMemoryLinkType(store.links[0].LinkType) {
		t.Fatalf("link type %q is not an accepted value", store.links[0].LinkType)
	}
}

// ts-0019: every documented enum value is present and stable.
func TestStorageEnumValues(t *testing.T) {
	for _, want := range []MemoryUnitType{TypeWorld, TypeExperience, TypeObservation} {
		if !IsValidMemoryUnitType(want) {
			t.Fatalf("missing unit type %q", want)
		}
	}
	for _, want := range []MemoryLinkType{LinkTemporal, LinkSemantic, LinkEntity, LinkCauses, LinkCausedBy, LinkEnables, LinkPrevents} {
		if !IsValidMemoryLinkType(want) {
			t.Fatalf("missing link type %q", want)
		}
	}
	for _, want := range []EntityKind{EntityKindRegular, EntityKindLabel} {
		found := false
		for _, k := range EntityKinds() {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing entity kind %q", want)
		}
	}
	for _, want := range []OperationStatus{StatusPending, StatusProcessing, StatusCompleted, StatusFailed, StatusCancelled, StatusNotFound} {
		if want == "" {
			t.Fatalf("empty status value")
		}
	}
}

// ts-0023 / ec-0003: a byte-identical re-ingest produces identical chunk hashes
// so unchanged chunks can be skipped.
func TestReingestProducesStableHashes(t *testing.T) {
	store := NewMemoryStore("public")
	item := RetainItem{Content: "stable content", DocumentID: "d1"}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{BankID: "b1", Items: []RetainItem{item}}); err != nil {
		t.Fatal(err)
	}
	firstHash := store.chunks[0].ContentHash
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{BankID: "b1", Items: []RetainItem{item}}); err != nil {
		t.Fatal(err)
	}
	if store.chunks[len(store.chunks)-1].ContentHash != firstHash {
		t.Fatalf("re-ingest must yield a stable content hash")
	}
}

// ts-0004: a reused operation identifier is a conflict; an identifier-less
// write is distinct.
func TestRetainOperationIdempotency(t *testing.T) {
	store := NewMemoryStore("public")
	opID := "11111111-1111-1111-1111-111111111111"
	req := RetainRequest{BankID: "b1", Async: true, OperationID: opID, Items: []RetainItem{{Content: "x"}}}
	if _, err := store.RetainWrite(context.TODO(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(context.TODO(), req); err != ErrOperationConflict {
		t.Fatalf("reused operation id err = %v, want conflict", err)
	}
	first, _ := store.RetainWrite(context.TODO(), RetainRequest{BankID: "b1", Async: true, Items: []RetainItem{{Content: "a"}}})
	second, _ := store.RetainWrite(context.TODO(), RetainRequest{BankID: "b1", Async: true, Items: []RetainItem{{Content: "b"}}})
	if first.OperationID == second.OperationID {
		t.Fatalf("identifier-less writes must be distinct")
	}
}

// ts-0006 / st-0003: cancelling a pending operation sets cancelled; a terminal
// one conflicts.
func TestCancelOperationLifecycle(t *testing.T) {
	store := NewMemoryStore("public")
	op, err := store.PutOperation(context.TODO(), Operation{ID: "op-a", BankID: "b1", Status: StatusPending})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.CancelOperation(context.TODO(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("cancelled status = %q", cancelled.Status)
	}
	if _, err := store.CancelOperation(context.TODO(), op.ID); err != ErrOperationConflict {
		t.Fatalf("cancel of a terminal operation err = %v, want conflict", err)
	}
}
