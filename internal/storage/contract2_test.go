package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The knowledge base stores folders and pages, updates them, searches them and
// deletes them.
func TestContractKnowledgeBase(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()

	if _, err := store.GetKnowledgeNode(ctx, bank, "missing"); !errors.Is(err, ErrKnowledgeNodeNotFound) {
		t.Fatalf("unknown node = %v, want ErrKnowledgeNodeNotFound", err)
	}
	folder, err := store.PutKnowledgeNode(ctx, KnowledgeNode{ID: "n1", BankID: bank, Kind: KnowledgeKindFolder, Title: "Runbooks"})
	if err != nil {
		t.Fatalf("put folder: %v", err)
	}
	page, err := store.PutKnowledgeNode(ctx, KnowledgeNode{
		ID: "n2", BankID: bank, ParentID: folder.ID, Kind: KnowledgeKindPage,
		Title: "Deploy", Text: "run the pipeline", Tags: []string{"ops"},
	})
	if err != nil {
		t.Fatalf("put page: %v", err)
	}
	// A node without a valid kind is rejected.
	if _, err := store.PutKnowledgeNode(ctx, KnowledgeNode{ID: "n3", BankID: bank, Kind: "bogus"}); err == nil {
		t.Fatal("an unknown kind must be rejected")
	}

	nodes, err := store.ListKnowledgeNodes(ctx, bank)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("list nodes = %v (err %v)", nodes, err)
	}
	title := "Deployment"
	updated, err := store.UpdateKnowledgeNode(ctx, bank, page.ID, KnowledgeNodePatch{Title: &title})
	if err != nil || updated.Title != "Deployment" {
		t.Fatalf("update node = %+v (err %v)", updated, err)
	}
	if hits, err := store.SearchKnowledgeNodes(ctx, bank, "pipeline", 10); err != nil || len(hits) == 0 {
		t.Fatalf("search nodes = %v (err %v)", hits, err)
	}
	if err := store.DeleteKnowledgeNode(ctx, bank, page.ID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	if _, err := store.GetKnowledgeNode(ctx, bank, page.ID); !errors.Is(err, ErrKnowledgeNodeNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

// The mental-model lifecycle stores the model, records a refresh in its
// history, clears its content and deletes it.
func TestContractMentalModels(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()

	if _, err := store.GetMentalModel(ctx, bank, "missing"); !errors.Is(err, ErrMentalModelNotFound) {
		t.Fatalf("unknown model = %v, want ErrMentalModelNotFound", err)
	}
	model, err := store.PutMentalModel(ctx, MentalModel{
		ID: "m1", BankID: bank, Subtype: SubtypePinned, Name: "goals",
		SourceQuery: "what are the goals?", Content: "ship v1",
	})
	if err != nil {
		t.Fatalf("put model: %v", err)
	}
	// A model with an unknown subtype is rejected, which is the store's own
	// validation rather than a handler's.
	if _, err := store.PutMentalModel(ctx, MentalModel{ID: "m2", BankID: bank, Subtype: "bogus"}); !errors.Is(err, ErrInvalidMentalModelSubtype) {
		t.Fatalf("unknown subtype = %v, want ErrInvalidMentalModelSubtype", err)
	}

	models, err := store.ListMentalModels(ctx, bank, SubtypePinned)
	if err != nil || len(models) != 1 {
		t.Fatalf("list models = %v (err %v)", models, err)
	}
	description := "the current goals"
	updated, err := store.UpdateMentalModel(ctx, bank, model.ID, MentalModelPatch{Description: &description})
	if err != nil || updated.Description != description {
		t.Fatalf("update model = %+v (err %v)", updated, err)
	}
	if err := store.RecordMentalModelHistory(ctx, bank, model.ID, MentalModelHistoryEntry{
		RefreshedAt: time.Now().UTC(), SourceQuery: model.SourceQuery, Content: "refreshed", Reason: "refresh",
	}); err != nil {
		t.Fatalf("record history: %v", err)
	}
	history, err := store.MentalModelHistory(ctx, bank, model.ID)
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %v (err %v)", history, err)
	}
	cleared, err := store.ClearMentalModel(ctx, bank, model.ID)
	if err != nil || cleared.Content != "" {
		t.Fatalf("clear model = %+v (err %v)", cleared, err)
	}
	if err := store.DeleteMentalModel(ctx, bank, model.ID); err != nil {
		t.Fatalf("delete model: %v", err)
	}
	if _, err := store.GetMentalModel(ctx, bank, model.ID); !errors.Is(err, ErrMentalModelNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

// The observability routes summarize a bank and list its audit and LLM records.
func TestContractObservability(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items:  []RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := store.BankStats(ctx, bank)
	if err != nil {
		t.Fatalf("bank stats: %v", err)
	}
	if stats.BankID != bank || stats.TotalNodes == 0 {
		t.Fatalf("stats = %+v", stats)
	}

	if _, err := store.MemoriesTimeseries(ctx, bank, "day", "created_at"); err != nil {
		t.Fatalf("timeseries: %v", err)
	}
	if _, err := store.ListAuditLog(ctx, AuditQuery{BankID: bank, Limit: 10}); err != nil {
		t.Fatalf("audit log: %v", err)
	}
	if _, err := store.AuditLogStats(ctx, bank, "day"); err != nil {
		t.Fatalf("audit stats: %v", err)
	}
	if _, err := store.ListLLMRequests(ctx, LLMRequestQuery{BankID: bank, Limit: 10}); err != nil {
		t.Fatalf("llm requests: %v", err)
	}
	if _, err := store.LLMRequestStats(ctx, bank, "day"); err != nil {
		t.Fatalf("llm stats: %v", err)
	}
	scopes, err := store.ListObservationScopes(ctx, bank, 10, 0)
	if err != nil {
		t.Fatalf("observation scopes: %v", err)
	}
	if scopes.Scopes == nil {
		t.Fatal("scopes must be an array, not nil")
	}
}

// The operation lifecycle advances through its states and reports the sentinel
// for an unknown identifier.
func TestContractOperationLifecycle(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	if _, err := store.GetOperation(ctx, "missing"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("unknown operation = %v, want ErrOperationNotFound", err)
	}
	if _, err := store.PutOperation(ctx, Operation{
		ID: "op-1", BankID: bank, OperationType: OperationTypeRetain, Status: StatusPending,
	}); err != nil {
		t.Fatalf("put operation: %v", err)
	}
	advanced, err := store.AdvanceOperation(ctx, "op-1", StatusCompleted, "")
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if advanced.Status != StatusCompleted {
		t.Fatalf("advanced status = %q", advanced.Status)
	}
	page, err := store.ListOperations(ctx, OperationQuery{BankID: bank, Limit: 10})
	if err != nil || page.Total != 1 {
		t.Fatalf("list operations = %+v (err %v)", page, err)
	}
}

// The memory sub-route methods read, update and record history for a unit, and
// enqueue and recover consolidation passes.
func TestContractMemorySubRoutes(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items:  []RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryID := outcome.ItemUnitIDs[0][0]

	if _, err := store.GetMemory(ctx, bank, "missing"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("unknown memory = %v, want ErrMemoryNotFound", err)
	}
	unit, err := store.GetMemory(ctx, bank, memoryID)
	if err != nil || unit.ID != memoryID {
		t.Fatalf("get memory = %+v (err %v)", unit, err)
	}
	text := "Alice works at Google on the AI team."
	updated, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Text: &text})
	if err != nil || updated.Text != text {
		t.Fatalf("update memory = %+v (err %v)", updated, err)
	}
	if _, err := store.MemoryHistory(ctx, bank, memoryID); err != nil {
		t.Fatalf("memory history: %v", err)
	}
	if _, err := store.DeleteObservations(ctx, bank, memoryID); err != nil {
		t.Fatalf("delete observations: %v", err)
	}

	op, deduplicated, err := store.EnqueueConsolidation(ctx, bank)
	if err != nil || op.ID == "" {
		t.Fatalf("enqueue consolidation = %+v (err %v)", op, err)
	}
	// A second enqueue while one is pending is reported as deduplicated.
	if _, again, err := store.EnqueueConsolidation(ctx, bank); err != nil || !again {
		t.Fatalf("second enqueue = deduplicated %v (err %v), want true", again, err)
	}
	_ = deduplicated
	if _, err := store.RecoverConsolidations(ctx, bank); err != nil {
		t.Fatalf("recover consolidations: %v", err)
	}
}
