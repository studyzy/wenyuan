//go:build pgx

package storage

import (
	"context"
	"testing"
)

// postgresModelEmbedding reads the stored vector literal of one mental model,
// because the shared read projection does not carry the column back.
func postgresModelEmbedding(t *testing.T, store Store, bankID, id string) string {
	t.Helper()
	pg := store.(*PostgresStore)
	var raw string
	if err := pg.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(embedding::text, '') FROM `+quoteIdent(pg.schema)+`.mental_models WHERE bank_id = $1 AND id = $2`,
		bankID, id).Scan(&raw); err != nil {
		t.Fatalf("read embedding: %v", err)
	}
	return raw
}

// test-postgres-clear-rebuilds-embedding: the PostgreSQL clear writes the
// name-only vector into the column, so the rebuild is not memory-backend-only.
func TestPostgresClearRebuildsEmbedding(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	embedder := &textEmbedder{}
	store.(interface{ SetObservationEmbedder(EmbeddingGenerator) }).SetObservationEmbedder(embedder)

	model, err := store.PutMentalModel(ctx, MentalModel{
		ID: "mm-clear", BankID: "vec-bank", Subtype: SubtypePinned, Name: "Order Operations",
		Content: "ship the new pipeline", Embedding: unitVector(0),
	})
	if err != nil {
		t.Fatalf("put model: %v", err)
	}
	if _, err := store.ClearMentalModel(ctx, "vec-bank", model.ID); err != nil {
		t.Fatalf("clear model: %v", err)
	}
	want := EncodeEmbedding(embedOne(t, embedder, "Order Operations "))
	if got := postgresModelEmbedding(t, store, "vec-bank", model.ID); got != want {
		t.Fatalf("cleared embedding = %q, want the name-only vector %q", got, want)
	}
}

// test-postgres-rename-reembeds-page: a knowledge-page rename reaches the
// backing model and rewrites its vector from the new name and the stored
// content.
func TestPostgresKnowledgeRenameReembeds(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	embedder := &textEmbedder{}
	store.(interface{ SetObservationEmbedder(EmbeddingGenerator) }).SetObservationEmbedder(embedder)

	model, err := store.PutMentalModel(ctx, MentalModel{
		ID: "mm-rename", BankID: "vec-bank", Subtype: SubtypePinned, Name: "Order Operations",
		Content: "ship the new pipeline",
	})
	if err != nil {
		t.Fatalf("put model: %v", err)
	}
	if _, err := store.PutKnowledgeNode(ctx, KnowledgeNode{
		ID: "kp-rename", BankID: "vec-bank", Kind: KnowledgeKindPage, Title: "Order Operations",
		Text: "ship the new pipeline", MentalModelID: model.ID,
	}); err != nil {
		t.Fatalf("put node: %v", err)
	}
	renamed := "Fulfilment Operations"
	if _, err := store.UpdateKnowledgeNode(ctx, "vec-bank", "kp-rename", KnowledgeNodePatch{Title: &renamed}); err != nil {
		t.Fatalf("rename node: %v", err)
	}
	got, err := store.GetMentalModel(ctx, "vec-bank", model.ID)
	if err != nil {
		t.Fatalf("get model: %v", err)
	}
	if got.Name != renamed {
		t.Fatalf("model name = %q, want %q", got.Name, renamed)
	}
	want := EncodeEmbedding(embedOne(t, embedder, renamed+" ship the new pipeline"))
	if stored := postgresModelEmbedding(t, store, "vec-bank", model.ID); stored != want {
		t.Fatalf("re-embedded vector = %q, want %q", stored, want)
	}
}

// test-postgres-create-embeds-from-name: the column is written from the model's
// own text on create, so a page created through the API is reachable through
// the vector arm of knowledge search — the reference sets it at create (its
// mental_models row carries a 384-dim vector for content = ”).
func TestPostgresCreateEmbedsFromName(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	embedder := &textEmbedder{}
	store.(interface{ SetObservationEmbedder(EmbeddingGenerator) }).SetObservationEmbedder(embedder)

	if _, err := store.PutMentalModel(ctx, MentalModel{
		ID: "mm-create", BankID: "vec-bank", Subtype: SubtypePinned, Name: "Pipeline notes",
	}); err != nil {
		t.Fatalf("put model: %v", err)
	}
	if got := postgresModelEmbedding(t, store, "vec-bank", "mm-create"); got == "" {
		t.Fatal("embedding is NULL after create, want the name's vector")
	}
	// A rename re-embeds from the new name plus the stored content.
	name := "Rotated pipeline notes"
	if _, err := store.UpdateMentalModel(ctx, "vec-bank", "mm-create", MentalModelPatch{Name: &name}); err != nil {
		t.Fatalf("update model: %v", err)
	}
	if got := postgresModelEmbedding(t, store, "vec-bank", "mm-create"); got == "" {
		t.Fatal("embedding is NULL after rename")
	}
}
