//go:build pgx

package storage

import (
	"context"
	"testing"
)

// A write return must not leak the derived is_knowledge_page flag: the
// reference's patch and clear queries project their row without the flag and
// _row_to_mental_model sets the key only when the row carries it, so a write
// answer is null even for a page's backing model, while every read keeps
// deriving it.
func TestPostgresMentalModelWriteReturnOmitsKnowledgePageFlag(t *testing.T) {
	pg := fullRefreshStore(t)
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutMentalModel(ctx, MentalModel{
		ID: "mm-page", BankID: "b1", Subtype: SubtypePinned, Name: "page", SourceQuery: "q",
		Content: "written",
	}); err != nil {
		t.Fatal(err)
	}
	// A page backs the model, so every read derives the flag.
	if _, err := pg.PutKnowledgeNode(ctx, KnowledgeNode{
		ID: "kp-1", BankID: "b1", Kind: KnowledgeKindPage, Title: "page",
		MentalModelID: "mm-page", Text: "written",
	}); err != nil {
		t.Fatal(err)
	}
	read, err := pg.GetMentalModel(ctx, "b1", "mm-page")
	if err != nil {
		t.Fatal(err)
	}
	if read.IsKnowledgePage == nil || !*read.IsKnowledgePage {
		t.Fatalf("read is_knowledge_page = %#v, want derived true", read.IsKnowledgePage)
	}

	patched, err := pg.UpdateMentalModel(ctx, "b1", "mm-page", MentalModelPatch{
		Name: strPtr("renamed"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if patched.IsKnowledgePage != nil {
		t.Fatalf("patch is_knowledge_page = %v, want null on the write return", *patched.IsKnowledgePage)
	}

	cleared, err := pg.ClearMentalModel(ctx, "b1", "mm-page")
	if err != nil {
		t.Fatal(err)
	}
	if cleared.IsKnowledgePage != nil {
		t.Fatalf("clear is_knowledge_page = %v, want null on the write return", *cleared.IsKnowledgePage)
	}

	// The reads still derive the flag after both writes.
	listed, err := pg.ListMentalModels(ctx, "b1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].IsKnowledgePage == nil || !*listed[0].IsKnowledgePage {
		t.Fatalf("list is_knowledge_page = %#v, want derived true", listed)
	}
	// A standalone model still reads derived false, not null.
	if _, err := pg.PutMentalModel(ctx, MentalModel{
		ID: "mm-note", BankID: "b1", Subtype: SubtypePinned, Name: "note", SourceQuery: "n",
	}); err != nil {
		t.Fatal(err)
	}
	note, err := pg.GetMentalModel(ctx, "b1", "mm-note")
	if err != nil {
		t.Fatal(err)
	}
	if note.IsKnowledgePage == nil || *note.IsKnowledgePage {
		t.Fatalf("standalone is_knowledge_page = %#v, want derived false", note.IsKnowledgePage)
	}
}
