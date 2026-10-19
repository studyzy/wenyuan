package memoryops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// ts-0048: a document reprocess submits an asynchronous batch retain — a
// pending parent batch_retain operation with a child retain — rather than
// extracting inside the request, and the queued child replays every retain
// parameter the document stored.
func TestReprocessSubmitsAsyncBatchReplayingParams(t *testing.T) {
	ctx := context.TODO()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: "b1",
		Items: []storage.RetainItem{{
			Content:           "Alice met Bob.",
			DocumentID:        "d1",
			Context:           "ctx",
			Strategy:          "strategy",
			Metadata:          map[string]any{"src": "slack"},
			Entities:          []storage.RetainEntity{{Text: "Alice", Kind: "PERSON"}},
			ResolveEntities:   false,
			ObservationScopes: json.RawMessage(`"per_tag"`),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)

	outcome, err := h.ReprocessDocument(ctx, "b1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.OperationID == "" {
		t.Fatal("reprocess returned no operation id")
	}

	parent, err := store.GetOperation(ctx, "b1", outcome.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.OperationType != storage.AsyncRetainType || parent.Status != storage.StatusPending || !parent.IsParent {
		t.Fatalf("parent = type %q status %q is_parent %v, want a pending batch_retain parent",
			parent.OperationType, parent.Status, parent.IsParent)
	}

	children, err := store.GetOperationChildren(ctx, "b1", outcome.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("parent has %d children, want 1", len(children))
	}
	child, err := store.GetOperation(ctx, "b1", children[0].OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if child.OperationType != storage.OperationTypeRetain {
		t.Fatalf("child type = %q, want %q", child.OperationType, storage.OperationTypeRetain)
	}
	req, err := storage.DecodeAsyncRetainPayload(child.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Items) != 1 {
		t.Fatalf("child carries %d items, want 1", len(req.Items))
	}
	item := req.Items[0]
	if !item.ForceReextract {
		t.Fatal("replayed item does not force a re-extract")
	}
	if item.Content != "Alice met Bob." || item.DocumentID != "d1" || item.UpdateMode != "replace" {
		t.Fatalf("replayed identity = %q/%q/%q", item.Content, item.DocumentID, item.UpdateMode)
	}
	if item.Context != "ctx" || item.Strategy != "strategy" {
		t.Fatalf("replayed context/strategy = %q/%q", item.Context, item.Strategy)
	}
	if item.Metadata["src"] != "slack" {
		t.Fatalf("replayed metadata = %#v", item.Metadata)
	}
	if len(item.Entities) != 1 || item.Entities[0].Text != "Alice" || item.Entities[0].Kind != "PERSON" {
		t.Fatalf("replayed entities = %#v", item.Entities)
	}
	if item.ResolveEntities {
		t.Fatal("replayed resolve_entities = true, want false")
	}
	if string(item.ObservationScopes) != `"per_tag"` {
		t.Fatalf("replayed observation_scopes = %s", item.ObservationScopes)
	}
}

// The names a caller gave its attachments ride the document's replayable
// parameters, so a reprocess restates them instead of losing them: a write
// replaces the document record, and the reference keeps the names by replaying
// them. The parameter round-trips through the stored map and back onto the item
// a reprocess writes.
func TestReprocessReplaysAttachmentFilenames(t *testing.T) {
	params := map[string]any{
		"attachment_filenames": map[string]any{"abc123": "plan.txt", "def456": "chart.png"},
	}
	item := storage.RetainItem{}
	applyRetainParams(&item, params)
	if len(item.AttachmentFilenames) != 2 || item.AttachmentFilenames["abc123"] != "plan.txt" {
		t.Fatalf("filenames not replayed: %+v", item.AttachmentFilenames)
	}
	// The postgres store hands the map back as map[string]string, because the
	// decodeJSONMap keeper keeps a nested object it can type directly.
	direct := storage.RetainItem{}
	applyRetainParams(&direct, map[string]any{
		"attachment_filenames": map[string]string{"abc123": "plan.txt"},
	})
	if direct.AttachmentFilenames["abc123"] != "plan.txt" {
		t.Fatalf("typed map not replayed: %+v", direct.AttachmentFilenames)
	}
	// A malformed entry is dropped rather than failing the reprocess.
	broken := storage.RetainItem{}
	applyRetainParams(&broken, map[string]any{
		"attachment_filenames": map[string]any{"abc123": 5, "": "plan.txt"},
	})
	if len(broken.AttachmentFilenames) != 0 {
		t.Fatalf("malformed names kept: %+v", broken.AttachmentFilenames)
	}
}
