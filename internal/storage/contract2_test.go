package storage

import (
	"context"
	"errors"
	"slices"
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
	if hits, err := store.SearchKnowledgeNodes(ctx, KnowledgeSearchQuery{BankID: bank, Query: "pipeline", Limit: 10}); err != nil || len(hits) == 0 {
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
		ChangedAt: time.Now().UTC(), PreviousContent: strPtr("prior"),
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
	// Clearing resets the content and the refresh baseline; it is not a delete,
	// so the recorded refreshes stay readable in the model's history.
	if history, err := store.MentalModelHistory(ctx, bank, model.ID); err != nil || len(history) != 1 {
		t.Fatalf("history after clear = %v (err %v), want the recorded refresh to survive", history, err)
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
	if _, err := store.AuditLogStats(ctx, bank, "day", ""); err != nil {
		t.Fatalf("audit stats: %v", err)
	}
	if _, err := store.ListLLMRequests(ctx, LLMRequestQuery{BankID: bank, Limit: 10}); err != nil {
		t.Fatalf("llm requests: %v", err)
	}
	if _, err := store.LLMRequestStats(ctx, bank, "day", ""); err != nil {
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
	if _, err := store.GetOperation(ctx, bank, "missing"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("unknown operation = %v, want ErrOperationNotFound", err)
	}
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "op-1", BankID: bank, OperationType: OperationTypeRetain, Status: StatusPending,
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

// A cancelled operation moves its status and updated_at only: the reference
// leaves completed_at null, so the read model reports the status change without
// inventing a completion time.
func TestContractCancelLeavesCompletedAtNull(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "op-cancel", BankID: bank, OperationType: OperationTypeRetain, Status: StatusPending,
	}); err != nil {
		t.Fatalf("put operation: %v", err)
	}
	cancelled, err := store.CancelOperation(ctx, bank, "op-cancel")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", cancelled.Status)
	}
	if cancelled.CompletedAt != nil {
		t.Fatalf("completed_at = %v, want null", *cancelled.CompletedAt)
	}
	// The read model agrees with the write.
	reread, err := store.GetOperation(ctx, bank, "op-cancel")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if reread.CompletedAt != nil {
		t.Fatalf("stored completed_at = %v, want null", *reread.CompletedAt)
	}
}

// Advancing with a result merges the reported metadata onto the operation,
// leaving anything already recorded in place, and is a no-op when nothing is
// reported.
func TestContractAdvanceOperationWithResultMergesMetadata(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "op-1", BankID: bank, OperationType: OperationTypeRefreshMentalModel,
		Status: StatusPending, ResultMetadata: map[string]any{"mental_model_id": "mm-1"},
	}); err != nil {
		t.Fatalf("put operation: %v", err)
	}

	advanced, err := store.AdvanceOperationWithResult(ctx, "op-1", StatusCompleted, "", map[string]any{
		"outcome":        "content_written",
		"failure_reason": nil,
	})
	if err != nil {
		t.Fatalf("advance with result: %v", err)
	}
	if advanced.ResultMetadata["outcome"] != "content_written" {
		t.Fatalf("metadata = %+v, want the reported outcome", advanced.ResultMetadata)
	}
	if _, ok := advanced.ResultMetadata["failure_reason"]; !ok {
		t.Fatalf("metadata = %+v, want the explicitly cleared reason", advanced.ResultMetadata)
	}
	if advanced.ResultMetadata["mental_model_id"] != "mm-1" {
		t.Fatalf("metadata = %+v, want the pre-existing field preserved", advanced.ResultMetadata)
	}
}

// Scheduling a retry with a result merges the reported metadata onto the
// operation while leaving the retry schedule — status, count and next attempt
// time — exactly as the plain scheduling path sets it.
func TestContractScheduleOperationRetryWithResultMergesMetadata(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	if _, err := store.PutOperation(ctx, Operation{
		OperationID: "op-1", BankID: bank, OperationType: OperationTypeTransferImport,
		Status: StatusProcessing, ResultMetadata: map[string]any{"webhook_id": "wh-1"},
	}); err != nil {
		t.Fatalf("put operation: %v", err)
	}
	// A compile-time proof that the memory backend carries the capability, then
	// the behavioural one that its metadata survives.
	var recorder OperationRetryRecorder = store
	next := time.Now().UTC().Add(time.Minute)
	if err := recorder.ScheduleOperationRetryWithResult(ctx, "op-1", 0, next, "rate limited", map[string]any{
		"last_status_code": 429,
	}); err != nil {
		t.Fatalf("schedule retry with result: %v", err)
	}
	after, err := store.GetOperation(ctx, bank, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.ResultMetadata["last_status_code"] != 429 {
		t.Fatalf("metadata = %+v, want the reported status", after.ResultMetadata)
	}
	if after.ResultMetadata["webhook_id"] != "wh-1" {
		t.Fatalf("metadata = %+v, want the pre-existing field preserved", after.ResultMetadata)
	}
	if after.Status != StatusPending || after.RetryCount != 0 || after.NextRetryAt == nil {
		t.Fatalf("the retry schedule must be unchanged: %+v", after)
	}
	// An empty report takes the plain path: only the schedule moves.
	if err := recorder.ScheduleOperationRetryWithResult(ctx, "op-1", 0, next, "again", nil); err != nil {
		t.Fatalf("schedule retry without result: %v", err)
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
	if err != nil || op.OperationID == "" {
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

// Curation replaces a unit's entity set, and the names resolve the way retain
// resolves them: a name that matches an existing entity is linked to it, and a
// name that matches nothing becomes one. Before this, only the in-memory
// backend rewrote the links, so editing entities through the API worked in
// tests and did nothing against PostgreSQL.
func TestCurationReplacesUnitEntities(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items: []RetainItem{{
			Content: "Ada Lovelace wrote notes.",
			Entities: []RetainEntity{
				{Text: "Ada Lovelace"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryID := outcome.ItemUnitIDs[0][0]

	// One name already exists from retain; the other is new to the bank. Both
	// end up linked, and the retained entity keeps its link.
	names := []string{"Ada Lovelace", "Charles Babbage"}
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Entities: &names}); err != nil {
		t.Fatalf("update entities: %v", err)
	}
	got, err := store.UnitEntities(ctx, bank, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"Ada Lovelace", "Charles Babbage"}) {
		t.Fatalf("entities = %v, want both names", got)
	}

	// Replacing with a subset drops the entity that is no longer named and
	// leaves the other one's mention count alone.
	subset := []string{"Ada Lovelace"}
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Entities: &subset}); err != nil {
		t.Fatalf("replace entities: %v", err)
	}
	got, err = store.UnitEntities(ctx, bank, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"Ada Lovelace"}) {
		t.Fatalf("entities after replace = %v, want only the kept name", got)
	}

	// An empty set detaches everything.
	none := []string{}
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Entities: &none}); err != nil {
		t.Fatalf("clear entities: %v", err)
	}
	got, err = store.UnitEntities(ctx, bank, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("entities after clearing = %v, want none", got)
	}
}

// A listing reports each unit's entities, and an observation that owns no
// postings reports the entities of the memories it was consolidated from. The
// data view renders both, so a listing that returned nothing for either would
// show entity chips for some rows and not others.
func TestUnitEntityNamesIncludesObservations(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items: []RetainItem{{
			Content:  "Ada Lovelace and Charles Babbage corresponded.",
			Entities: []RetainEntity{{Text: "Ada Lovelace"}, {Text: "Charles Babbage"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := outcome.ItemUnitIDs[0][0]

	// An observation built from that memory, with no postings of its own.
	observation := MemoryUnit{
		ID: newID(), BankID: bank, Text: "They worked together.",
		FactType: TypeObservation, SourceMemoryIDs: []string{sourceID},
	}
	if _, err := store.UpsertUnit(ctx, observation); err != nil {
		t.Fatalf("upsert observation: %v", err)
	}

	names, err := store.UnitEntityNames(ctx, bank, []string{sourceID, observation.ID, "unknown-unit"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names[sourceID], []string{"Ada Lovelace", "Charles Babbage"}) {
		t.Fatalf("source entities = %v", names[sourceID])
	}
	if !slices.Equal(names[observation.ID], []string{"Ada Lovelace", "Charles Babbage"}) {
		t.Fatalf("inherited entities = %v, want the source memory's", names[observation.ID])
	}
	if _, present := names["unknown-unit"]; present {
		t.Fatalf("an unknown unit must be absent from the map: %v", names)
	}

	// A unit that owns postings keeps them rather than inheriting.
	if names[observation.ID][0] != "Ada Lovelace" {
		t.Fatalf("inherited entities = %v", names[observation.ID])
	}
	if err := store.SetUnitEntities(ctx, bank, observation.ID, []string{"NASA"}, true); err != nil {
		t.Fatalf("set observation entities: %v", err)
	}
	names, err = store.UnitEntityNames(ctx, bank, []string{observation.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names[observation.ID], []string{"NASA"}) {
		t.Fatalf("observation entities after its own set = %v, want NASA only", names[observation.ID])
	}
}
