package readsurface

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A PATCH stamps the documents row: the reference writes `updated_at = now()`
// beside the fields it sets on every update, whether or not the update changes
// anything (documents.py:386-400).
func TestDocumentPatchStampsUpdatedAt(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	if _, err := store.PutDocument(ctx, bank, "doc-1", storage.Document{OriginalText: "text"}); err != nil {
		t.Fatal(err)
	}
	mux := serve(t, store)
	url := "/v1/default/banks/" + bank + "/documents/doc-1"

	if rec := send(mux, http.MethodPatch, url, `{"tags":["fresh"]}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d (body %s)", rec.Code, rec.Body.String())
	}
	doc, err := store.GetDocument(ctx, bank, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.UpdatedAt.IsZero() {
		t.Fatal("PATCH left the documents row unstamped")
	}
	var detail DocumentResponse
	decodeInto(t, send(mux, http.MethodGet, url, ""), &detail)
	if detail.UpdatedAt == "" {
		t.Fatal("served updated_at is empty after a PATCH")
	}
}

// A tag array whose SET matches the current one is a semantic no-op even when
// the arrays differ as lists: the reference compares
// `set(current_tags) != set(tags)`, so a normalisation sweep that re-sends one
// fewer copy of a duplicated tag cascades nothing — the units keep the tags and
// observations they already carry.
func TestDocumentPatchSkipsCascadeForTagSetMatch(t *testing.T) {
	store := newBankStore(t)
	ctx := context.Background()
	unitID := "unit-1"
	if _, err := store.PutDocument(ctx, bank, "doc-1", storage.Document{
		OriginalText: "text", Tags: []string{"work", "work"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: unitID, BankID: bank, Text: "a fact", DocumentID: "doc-1",
		FactType: storage.TypeWorld, Tags: []string{"work", "work"},
	}); err != nil {
		t.Fatal(err)
	}
	mux := serve(t, store)

	if rec := send(mux, http.MethodPatch, "/v1/default/banks/"+bank+"/documents/doc-1", `{"tags":["work"]}`); rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d (body %s)", rec.Code, rec.Body.String())
	}
	unit, err := store.GetMemory(ctx, bank, unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unit.Tags) != 2 || unit.Tags[0] != "work" || unit.Tags[1] != "work" {
		t.Fatalf("unit tags after a set-equal PATCH = %v, want untouched [work work]", unit.Tags)
	}
	// The document itself carries the array exactly as it was sent.
	doc, err := store.GetDocument(ctx, bank, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Tags) != 1 || doc.Tags[0] != "work" {
		t.Fatalf("document tags = %v, want [work]", doc.Tags)
	}
}
