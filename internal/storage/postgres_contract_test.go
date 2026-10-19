//go:build pgx

package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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
	if _, err := store.SetAliasPrimary(ctx, "vec-bank", "work", true); err != nil {
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

// A duplicate alias the pre-check cannot see — because the name was resolved as
// absent before the conflicting row appeared — is caught by the primary key and
// reported as the reference's conflict rather than a raw write error.
func TestPostgresContractAliasInsertRace(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("store = %T, want *PostgresStore", store)
	}

	// Prime the cache with a negative resolution so the pre-check believes the
	// name is free while the row below already occupies it.
	if _, err := store.ResolveBankID(ctx, "dup-alias"); err != nil {
		t.Fatalf("prime negative resolve: %v", err)
	}
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(pg.schema)+`.bank_aliases (alias, bank_id, is_primary) VALUES ('dup-alias','vec-bank',false)`); err != nil {
		t.Fatalf("seed alias row: %v", err)
	}
	if _, err := store.AddAlias(ctx, "vec-bank", "dup-alias", false); !errors.Is(err, ErrAliasConflict) {
		t.Fatalf("racing alias insert = %v, want ErrAliasConflict", err)
	}
}

// An alias that names an existing bank is refused: the two live in different
// tables, so the conflict check has to read `banks` rather than rely on a
// constraint (the reference's INSERT ... WHERE NOT EXISTS does the same).
func TestPostgresContractAliasMatchesExistingBank(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	if _, err := store.CreateBank(ctx, Bank{BankID: "vec-bank-2", Name: "two"}); err != nil {
		t.Fatalf("create second bank: %v", err)
	}
	if _, err := store.AddAlias(ctx, "vec-bank", "vec-bank-2", false); !errors.Is(err, ErrAliasConflict) {
		t.Fatalf("alias naming an existing bank = %v, want ErrAliasConflict", err)
	}
	// The bank keeps its own identity: the refused alias did not create a row.
	if resolved, err := store.ResolveBankID(ctx, "vec-bank-2"); err != nil || resolved != "vec-bank-2" {
		t.Fatalf("resolve = %q (err %v), want the bank's own id", resolved, err)
	}
}

// The directive tags are stored byte-for-byte: the reference writes the
// caller's list straight into the text[] column, with no trimming or dropping.
func TestPostgresContractDirectiveTagsVerbatim(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	created, err := store.PutDirective(ctx, Directive{
		ID: "11111111-1111-1111-1111-111111111111", BankID: "vec-bank", Name: "n", Content: "c",
		Tags: []string{" a ", "", "b"},
	})
	if err != nil {
		t.Fatalf("put directive: %v", err)
	}
	if len(created.Tags) != 3 || created.Tags[0] != " a " || created.Tags[1] != "" || created.Tags[2] != "b" {
		t.Fatalf("stored tags = %#v, want the caller's list verbatim", created.Tags)
	}
	readBack, err := store.GetDirective(ctx, "vec-bank", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("get directive: %v", err)
	}
	if len(readBack.Tags) != 3 || readBack.Tags[0] != " a " || readBack.Tags[1] != "" || readBack.Tags[2] != "b" {
		t.Fatalf("read-back tags = %#v, want the caller's list verbatim", readBack.Tags)
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
	if _, err := store.GetChunk(ctx, "vec-bank", chunks.Chunks[0].ChunkID); err != nil {
		t.Fatalf("get chunk: %v", err)
	}
	if tags, err := store.ListTags(ctx, "vec-bank"); err != nil || len(tags) == 0 {
		t.Fatalf("list tags = %v (err %v)", tags, err)
	}
	if entities, err := store.ListEntities(ctx, EntityQuery{BankID: "vec-bank", Limit: 10}); err != nil || len(entities.Entities) == 0 {
		t.Fatalf("list entities = %d (err %v)", len(entities.Entities), err)
	}
	if _, err := store.Graph(ctx, GraphQuery{BankID: "vec-bank", Limit: 10}); err != nil {
		t.Fatalf("graph: %v", err)
	}
	if _, err := store.EntityGraph(ctx, EntityGraphQuery{BankID: "vec-bank", Limit: 10}); err != nil {
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
		ID: pgUUID("w1"), BankID: "vec-bank", URL: "https://example.test/hook",
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
	if deliveries, err := store.ListWebhookDeliveries(ctx, "vec-bank", webhook.ID, 10, ""); err != nil {
		t.Fatalf("list deliveries: %v", err)
	} else if deliveries.Deliveries == nil {
		t.Fatal("deliveries must be an array, not nil")
	}
	if _, err := store.ListWebhookDeliveries(ctx, "vec-bank", webhook.ID, 10, "not-a-timestamp"); err == nil ||
		!strings.Contains(err.Error(), "invalid input for query argument $3: 'not-a-timestamp'") {
		t.Fatalf("malformed cursor = %v, want the reference's adapter error", err)
	}
	if err := store.DeleteWebhook(ctx, "vec-bank", webhook.ID); err != nil {
		t.Fatalf("delete webhook: %v", err)
	}
	if _, err := store.GetWebhook(ctx, "vec-bank", webhook.ID); !errors.Is(err, ErrWebhookNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
	// A deleted webhook's delivery rows are historical facts and are retained.
	if _, err := store.ListWebhookDeliveries(ctx, "vec-bank", webhook.ID, 10, ""); err != nil {
		t.Fatalf("list deliveries after delete: %v", err)
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
	if hits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{BankID: "vec-bank", Query: "pipeline", Limit: 10}); err != nil || len(hits) == 0 {
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
		ChangedAt: model.CreatedAt, PreviousContent: strPtr("prior"),
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

// Scheduling a retry with a result against the real database merges the report
// onto the operation while leaving the retry schedule — status, count and next
// attempt time — exactly as the plain scheduling path sets it.
func TestPostgresContractScheduleRetryWithResult(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	// async_operations.operation_id is a uuid column in the reference schema.
	opID := pgUUID("op-retry")
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: opID, BankID: "vec-bank", OperationType: OperationTypeTransferImport,
		Status: StatusProcessing, ResultMetadata: map[string]any{"webhook_id": "wh-1"},
	}); err != nil {
		t.Fatalf("put operation: %v", err)
	}
	recorder := store.(OperationRetryRecorder)
	next := time.Now().UTC().Add(time.Minute)
	if err := recorder.ScheduleOperationRetryWithResult(ctx, opID, 0, next, "rate limited", map[string]any{
		"last_status_code": 429,
	}); err != nil {
		t.Fatalf("schedule retry with result: %v", err)
	}
	after, err := store.GetOperation(ctx, "vec-bank", opID)
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	// JSON numbers round-trip as float64.
	if after.ResultMetadata["last_status_code"] != float64(429) {
		t.Fatalf("metadata = %+v, want the reported status", after.ResultMetadata)
	}
	if after.ResultMetadata["webhook_id"] != "wh-1" {
		t.Fatalf("metadata = %+v, want the pre-existing field preserved", after.ResultMetadata)
	}
	if after.Status != StatusPending || after.RetryCount != 0 || after.NextRetryAt == nil {
		t.Fatalf("the retry schedule must be unchanged: %+v", after)
	}
}

// The memory listing and the memory graph honour a compound read scope: the
// resolved tag groups AND into the filter on the real SQL side, so a scoped
// reader sees only the memories its scope admits and the total agrees.
func TestPostgresContractReadScopeTagGroups(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	for _, item := range []RetainItem{
		{Content: "Kate is interviewing at another company.", DocumentID: "kate-doc", Tags: []string{"user:kate"}},
		{Content: "Dan is planning a production deploy.", DocumentID: "dan-doc", Tags: []string{"user:dan"}},
	} {
		if _, err := store.RetainWrite(ctx, RetainRequest{BankID: "vec-bank", Items: []RetainItem{item}}); err != nil {
			t.Fatalf("retain: %v", err)
		}
	}
	scope := []TagGroup{{Tags: []string{"user:dan", "kind:rule"}, Match: TagsMatchAnyStrict}}

	memories, err := store.ListMemories(ctx, MemoryQuery{BankID: "vec-bank", TagGroups: scope, Limit: 10})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if memories.Total != 1 || len(memories.Memories) != 1 || memories.Memories[0].DocumentID != "dan-doc" {
		t.Fatalf("scoped memory list = %+v, want only dan-doc", memories)
	}

	graph, err := store.Graph(ctx, GraphQuery{BankID: "vec-bank", TagGroups: scope, Limit: 10})
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if graph.Total != 1 || len(graph.TableRows) != 1 || graph.TableRows[0].DocumentID != "dan-doc" {
		t.Fatalf("scoped graph = %+v, want only dan-doc", graph)
	}
}

// A configured migration database is the handle the series runs over, and the
// serving handle stays untouched by it.
func TestMigrationRunsOverItsOwnHandle(t *testing.T) {
	serving := &recordingDB{}
	migration := &recordingDB{}
	store := NewPostgresStore(serving, "public")
	store.migrationDB = migration

	if target := store.migrateTarget(); target.db != DB(migration) {
		t.Fatalf("the series would run over the serving handle: %T", target.db)
	}
	// Without one the store migrates over its own handle.
	plain := NewPostgresStore(serving, "public")
	if target := plain.migrateTarget(); target.db != DB(serving) {
		t.Fatal("a store with no migration database did not use its own handle")
	}
}
