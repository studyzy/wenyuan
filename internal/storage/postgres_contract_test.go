//go:build pgx

package storage

import (
	"context"
	"errors"
	"testing"
)

// test-postgres-contract: the PostgreSQL backend honours the same contract the
// in-memory backend does. The memory backend is what every other package's
// tests run against, so a divergence between the two only surfaces in
// production unless it is checked here against a real database.
func TestPostgresContractAliasesAndBanks(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	if _, err := store.ResolveBankID(ctx, "vec-bank"); err != nil {
		t.Fatalf("resolve own identifier: %v", err)
	}
	if _, err := store.AddAlias(ctx, "vec-bank", "work", false); err != nil {
		t.Fatalf("add alias: %v", err)
	}
	resolved, err := store.ResolveBankID(ctx, "work")
	if err != nil || resolved != "vec-bank" {
		t.Fatalf("resolve alias = %q (err %v)", resolved, err)
	}
	aliases, err := store.ListAliases(ctx, "vec-bank")
	if err != nil || len(aliases) == 0 {
		t.Fatalf("list aliases = %v (err %v)", aliases, err)
	}
	if _, err := store.PromoteAlias(ctx, "vec-bank", "work"); err != nil {
		t.Fatalf("promote alias: %v", err)
	}
	if _, err := store.RemoveAlias(ctx, "vec-bank", "work"); err != nil {
		t.Fatalf("remove alias: %v", err)
	}

	renamed, err := store.UpdateBank(ctx, "vec-bank", BankPatch{Name: func() *string { s := "Renamed"; return &s }()})
	if err != nil || renamed.Name != "Renamed" {
		t.Fatalf("update bank = %+v (err %v)", renamed, err)
	}
	if _, err := store.ListBanks(ctx, BankQuery{Limit: 10}); err != nil {
		t.Fatalf("list banks: %v", err)
	}
}

// The read surface returns the bank's derived views from real rows.
func TestPostgresContractReadSurface(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: "vec-bank",
		Items: []RetainItem{{
			Content:    "Alice works at Google on the AI team.",
			DocumentID: "doc-1",
			Entities: []RetainEntity{
				{Text: "Alice", Kind: "PERSON"},
				{Text: "Google", Kind: "ORG"},
			},
			Tags: []string{"work"},
		}},
	}); err != nil {
		t.Fatalf("retain: %v", err)
	}

	chunks, err := store.ListChunks(ctx, ChunkQuery{BankID: "vec-bank", Limit: 10})
	if err != nil || len(chunks.Chunks) == 0 {
		t.Fatalf("list chunks = %d (err %v)", len(chunks.Chunks), err)
	}
	if _, err := store.GetChunk(ctx, "vec-bank", chunks.Chunks[0].ID); err != nil {
		t.Fatalf("get chunk: %v", err)
	}
	if tags, err := store.ListTags(ctx, "vec-bank"); err != nil || len(tags) == 0 {
		t.Fatalf("list tags = %v (err %v)", tags, err)
	}
	if entities, err := store.ListEntities(ctx, EntityQuery{BankID: "vec-bank", Limit: 10}); err != nil || len(entities.Entities) == 0 {
		t.Fatalf("list entities = %d (err %v)", len(entities.Entities), err)
	}
	if _, err := store.Graph(ctx, "vec-bank", 10); err != nil {
		t.Fatalf("graph: %v", err)
	}
	if _, err := store.EntityGraph(ctx, "vec-bank", 10); err != nil {
		t.Fatalf("entity graph: %v", err)
	}
}

// The webhook family stores, updates, lists and deletes, reporting the sentinel
// for an unknown resource.
func TestPostgresContractWebhooks(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	if _, err := store.GetWebhook(ctx, "vec-bank", "missing"); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("unknown webhook = %v, want ErrWebhookNotFound", err)
	}
	webhook, err := store.PutWebhook(ctx, Webhook{
		ID: "w1", BankID: "vec-bank", URL: "https://example.test/hook",
		EventTypes: []string{"memory.created"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("put webhook: %v", err)
	}
	if hooks, err := store.ListWebhooks(ctx, "vec-bank"); err != nil || len(hooks) == 0 {
		t.Fatalf("list webhooks = %v (err %v)", hooks, err)
	}
	enabled := false
	updated, err := store.UpdateWebhook(ctx, "vec-bank", webhook.ID, WebhookPatch{Enabled: &enabled})
	if err != nil || updated.Enabled {
		t.Fatalf("update webhook = %+v (err %v)", updated, err)
	}
	if deliveries, err := store.ListWebhookDeliveries(ctx, "vec-bank", webhook.ID, 10, 0); err != nil {
		t.Fatalf("list deliveries: %v", err)
	} else if deliveries.Deliveries == nil {
		t.Fatal("deliveries must be an array, not nil")
	}
	if err := store.DeleteWebhook(ctx, "vec-bank", webhook.ID); err != nil {
		t.Fatalf("delete webhook: %v", err)
	}
	if _, err := store.GetWebhook(ctx, "vec-bank", webhook.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

// Stored files round trip through the database.
func TestPostgresContractStoredFiles(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	key := StoredFileKeyForBank("vec-bank", "archive.zip")
	if err := store.PutStoredFile(ctx, StoredFile{Key: key, ContentType: "application/zip", Data: []byte("zip bytes")}); err != nil {
		t.Fatalf("put stored file: %v", err)
	}
	file, err := store.GetStoredFile(ctx, key)
	if err != nil || string(file.Data) != "zip bytes" {
		t.Fatalf("get stored file = %+v (err %v)", file, err)
	}
	if _, err := store.GetStoredFile(ctx, StoredFileKeyForBank("vec-bank", "absent.zip")); !errors.Is(err, ErrStoredFileNotFound) {
		t.Fatalf("absent key = %v, want ErrStoredFileNotFound", err)
	}
}

// The knowledge base and mental models round trip through the database.
func TestPostgresContractKnowledgeAndModels(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	folder, err := store.PutKnowledgeNode(ctx, KnowledgeNode{ID: "kn1", BankID: "vec-bank", Kind: KnowledgeKindFolder, Title: "Runbooks"})
	if err != nil {
		t.Fatalf("put knowledge folder: %v", err)
	}
	if _, err := store.PutKnowledgeNode(ctx, KnowledgeNode{
		ID: "kn2", BankID: "vec-bank", ParentID: folder.ID, Kind: KnowledgeKindPage, Title: "Deploy", Text: "run the pipeline",
	}); err != nil {
		t.Fatalf("put knowledge page: %v", err)
	}
	if nodes, err := store.ListKnowledgeNodes(ctx, "vec-bank"); err != nil || len(nodes) != 2 {
		t.Fatalf("list knowledge nodes = %d (err %v)", len(nodes), err)
	}
	if hits, err := store.SearchKnowledgeNodes(ctx, "vec-bank", "pipeline", 10); err != nil || len(hits) == 0 {
		t.Fatalf("search knowledge = %v (err %v)", hits, err)
	}

	model, err := store.PutMentalModel(ctx, MentalModel{
		ID: "mm1", BankID: "vec-bank", Subtype: SubtypePinned, Name: "goals", Content: "ship v1",
	})
	if err != nil {
		t.Fatalf("put mental model: %v", err)
	}
	if models, err := store.ListMentalModels(ctx, "vec-bank", SubtypePinned); err != nil || len(models) == 0 {
		t.Fatalf("list mental models = %v (err %v)", models, err)
	}
	if err := store.RecordMentalModelHistory(ctx, "vec-bank", model.ID, MentalModelHistoryEntry{
		RefreshedAt: model.CreatedAt, Content: "refreshed", Reason: "refresh",
	}); err != nil {
		t.Fatalf("record history: %v", err)
	}
	if _, err := store.MentalModelHistory(ctx, "vec-bank", model.ID); err != nil {
		t.Fatalf("mental model history: %v", err)
	}
	if _, err := store.ClearMentalModel(ctx, "vec-bank", model.ID); err != nil {
		t.Fatalf("clear mental model: %v", err)
	}
	if err := store.DeleteMentalModel(ctx, "vec-bank", model.ID); err != nil {
		t.Fatalf("delete mental model: %v", err)
	}
}
