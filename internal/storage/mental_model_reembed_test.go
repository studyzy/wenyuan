package storage

import (
	"context"
	"slices"
	"testing"
)

// embedOne embeds a single text through the generator, so a test can build the
// vector it expects a write path to have stored.
func embedOne(t *testing.T, generator EmbeddingGenerator, text string) []float32 {
	t.Helper()
	vecs, err := generator.EmbedPassages(context.Background(), []string{text})
	if err != nil || len(vecs) != 1 || len(vecs[0]) == 0 {
		t.Fatalf("embed %q = %v (err %v)", text, vecs, err)
	}
	return vecs[0]
}

// test-clear-rebuilds-embedding-from-name: clearing a mental model runs its
// vector through the embedder again, over the name alone, so a cleared page
// stops ranking on the content it no longer holds and stays findable by title.
func TestClearMentalModelRebuildsEmbeddingFromName(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	embedder := &textEmbedder{}
	store.SetObservationEmbedder(embedder)

	model, err := store.PutMentalModel(ctx, MentalModel{
		ID: "m1", BankID: bank, Subtype: SubtypePinned, Name: "Order Operations",
		Content: "ship the new pipeline", Embedding: make([]float32, EmbeddingDimensions),
	})
	if err != nil {
		t.Fatalf("put model: %v", err)
	}
	cleared, err := store.ClearMentalModel(ctx, bank, model.ID)
	if err != nil {
		t.Fatalf("clear model: %v", err)
	}
	want := embedOne(t, embedder, "Order Operations ")
	if !slices.Equal(cleared.Embedding, want) {
		t.Fatalf("cleared embedding = %v, want the name-only vector %v", cleared.Embedding, want)
	}
}

// test-knowledge-rename-reembeds-page: renaming a knowledge page carries the new
// name onto its backing mental model and rebuilds the model's vector from the
// new name and its content, so the hybrid search's vector arm matches the title
// the page now has rather than the one it no longer does.
func TestKnowledgeRenameReembedsBackingModel(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	embedder := &textEmbedder{}
	store.SetObservationEmbedder(embedder)

	model, err := store.PutMentalModel(ctx, MentalModel{
		ID: "m1", BankID: bank, Subtype: SubtypePinned, Name: "Order Operations",
		Content: "ship the new pipeline",
	})
	if err != nil {
		t.Fatalf("put model: %v", err)
	}
	if _, err := store.PutKnowledgeNode(ctx, KnowledgeNode{
		ID: "kp-1", BankID: bank, Kind: KnowledgeKindPage, Title: "Order Operations",
		MentalModelID: model.ID,
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}
	renamed := "Fulfilment Operations"
	if _, err := store.UpdateKnowledgeNode(ctx, bank, "kp-1", KnowledgeNodePatch{Title: &renamed}); err != nil {
		t.Fatalf("rename node: %v", err)
	}
	got, err := store.GetMentalModel(ctx, bank, model.ID)
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	if got.Name != renamed {
		t.Fatalf("model name = %q, want %q", got.Name, renamed)
	}
	want := embedOne(t, embedder, renamed+" ship the new pipeline")
	if !slices.Equal(got.Embedding, want) {
		t.Fatalf("re-embedded vector = %v, want %v", got.Embedding, want)
	}
}

// test-write-embeds-from-name-and-content: the reference embeds a mental model
// from its own text on every write, so a create carries the name's vector and a
// rename re-embeds from the new name plus the stored content — that vector is
// what makes a page reachable through the vector arm of knowledge search. A
// configured embedder therefore owns the column.
func TestPutMentalModelEmbedsFromNameAndContent(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	embedder := &textEmbedder{}
	store.SetObservationEmbedder(embedder)

	created, err := store.PutMentalModel(ctx, MentalModel{
		ID: "m1", BankID: bank, Subtype: SubtypePinned, Name: "Pipeline notes",
	})
	if err != nil {
		t.Fatalf("put model: %v", err)
	}
	if want := embedOne(t, embedder, "Pipeline notes "); !slices.Equal(created.Embedding, want) {
		t.Fatalf("created embedding = %v, want the name vector %v", created.Embedding, want)
	}

	// A rename re-embeds from the new name and the stored content.
	name := "Rotated pipeline notes"
	renamed, err := store.UpdateMentalModel(ctx, bank, "m1", MentalModelPatch{Name: &name})
	if err != nil {
		t.Fatalf("update model: %v", err)
	}
	if want := embedOne(t, embedder, "Rotated pipeline notes "); !slices.Equal(renamed.Embedding, want) {
		t.Fatalf("renamed embedding = %v, want %v", renamed.Embedding, want)
	}

	// With no embedder configured the caller's vector (or its absence) stands.
	bare, _ := newContractStore(t)
	plain, err := bare.PutMentalModel(ctx, MentalModel{ID: "m2", BankID: bank, Subtype: SubtypePinned, Name: "n"})
	if err != nil {
		t.Fatalf("put without embedder: %v", err)
	}
	if len(plain.Embedding) != 0 {
		t.Fatalf("embedding without a generator = %v, want none", plain.Embedding)
	}
}
