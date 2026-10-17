package storage

import (
	"context"
	"testing"
	"time"
)

// The in-memory bank summary derives its link total (including the derived
// entity category), consolidation backlog and write/consolidation watermarks
// from unit state, matching the postgres backend and the reference.
func TestMemoryBankStatsObservability(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	consolidated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	writeAt := time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC)
	failedAt := time.Date(2026, 1, 4, 3, 4, 5, 0, time.UTC)
	store.mu.Lock()
	store.units = map[string]MemoryUnit{
		"u1": {ID: "u1", BankID: "b1", FactType: TypeWorld, UpdatedAt: &writeAt},
		"u2": {ID: "u2", BankID: "b1", FactType: TypeWorld, UpdatedAt: &writeAt},
		"u3": {ID: "u3", BankID: "b1", FactType: TypeObservation, UpdatedAt: &writeAt, ConsolidatedAt: &consolidated},
		"u4": {ID: "u4", BankID: "b1", FactType: TypeExperience, ConsolidationFailedAt: &failedAt, UpdatedAt: &writeAt},
	}
	store.unitEntities = map[string][]string{
		"u1": {"e1"},
		"u2": {"e1"},
		"u3": {"e1"},
		"u4": {"e2"},
	}
	store.links = []MemoryLink{{BankID: "b1", FromUnitID: "u1", ToUnitID: "u2", LinkType: LinkSemantic}}
	store.operations = map[string]Operation{
		"op1": {OperationID: "op1", BankID: "b1", Status: StatusPending},
		"op2": {OperationID: "op2", BankID: "b1", Status: StatusProcessing},
		"op3": {OperationID: "op3", BankID: "b1", Status: StatusFailed},
	}
	store.mu.Unlock()

	stats, err := store.BankStats(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	// e1 is shared by 3 units -> LEAST(3-1, 10) = 2 ; e2 by one unit -> 0.
	if got := stats.LinksByLinkType[string(LinkEntity)]; got != 2 {
		t.Errorf("entity links = %d, want 2", got)
	}
	if got := stats.LinksByLinkType[string(LinkSemantic)]; got != 1 {
		t.Errorf("semantic links = %d, want 1", got)
	}
	if stats.TotalLinks != 3 {
		t.Errorf("total links = %d, want 3 (semantic 1 + entity 2)", stats.TotalLinks)
	}
	if stats.LinksByFactType == nil || len(stats.LinksByFactType) != 0 {
		t.Errorf("links_by_fact_type must be a non-nil empty map, got %v", stats.LinksByFactType)
	}
	if stats.LinksBreakdown == nil || len(stats.LinksBreakdown) != 0 {
		t.Errorf("links_breakdown must be a non-nil empty map, got %v", stats.LinksBreakdown)
	}
	if stats.PendingOperations != 1 {
		t.Errorf("pending operations = %d, want 1 (processing not counted)", stats.PendingOperations)
	}
	if stats.FailedOperations != 1 {
		t.Errorf("failed operations = %d, want 1", stats.FailedOperations)
	}
	// u1 and u2 are unconsolidated world facts -> pending; u4's experience fact
	// failed. The observation u3 is outside the consolidator's candidate set.
	if stats.PendingConsolidation != 2 {
		t.Errorf("pending consolidation = %d, want 2", stats.PendingConsolidation)
	}
	if stats.FailedConsolidation != 1 {
		t.Errorf("failed consolidation = %d, want 1", stats.FailedConsolidation)
	}
	if stats.LastMemoryWriteAt == nil || !stats.LastMemoryWriteAt.Equal(writeAt) {
		t.Errorf("last memory write = %v, want %v", stats.LastMemoryWriteAt, writeAt)
	}
	if stats.LastConsolidatedAt == nil || !stats.LastConsolidatedAt.Equal(consolidated) {
		t.Errorf("last consolidated = %v, want %v", stats.LastConsolidatedAt, consolidated)
	}
	if stats.TotalObservations != 1 {
		t.Errorf("total observations = %d, want 1", stats.TotalObservations)
	}
}

// A forced recompute bypasses the cached summary while an ordinary read serves
// it, the reference's refresh=true semantics.
func TestMemoryBankStatsForceRefreshBypassesCache(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	store.statsCache = newBankStatsCache(time.Minute, 0)
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BankStats(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.units["u1"] = MemoryUnit{ID: "u1", BankID: "b1", FactType: TypeWorld}
	store.mu.Unlock()

	cached, err := store.BankStats(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if cached.TotalNodes != 0 {
		t.Fatalf("ordinary read recomputed the summary: %+v", cached)
	}
	fresh, err := store.BankStatsRefreshing(ctx, "b1", true)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.TotalNodes != 1 {
		t.Fatalf("forced stats = %+v, want 1 node", fresh)
	}
	// The forced value also refreshed the cache for later callers.
	after, err := store.BankStats(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if after.TotalNodes != 1 {
		t.Fatalf("cached-after-refresh stats = %+v, want 1 node", after)
	}
}

// CountAsyncOperations groups non-terminal operations by type and status,
// excluding terminal states, mapping a null type to "unknown" and carrying the
// bank only when a per-bank breakdown is asked for.
func TestMemoryCountAsyncOperations(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	store.mu.Lock()
	store.operations = map[string]Operation{
		"op1": {OperationID: "op1", BankID: "b1", OperationType: "retain", Status: StatusPending},
		"op2": {OperationID: "op2", BankID: "b1", OperationType: "retain", Status: StatusPending},
		"op3": {OperationID: "op3", BankID: "b1", OperationType: "consolidate", Status: StatusProcessing},
		"op4": {OperationID: "op4", BankID: "b2", OperationType: "", Status: StatusFailed},
		"op5": {OperationID: "op5", BankID: "b1", OperationType: "retain", Status: StatusCompleted},
	}
	store.mu.Unlock()

	groups, err := store.CountAsyncOperations(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := map[[2]string]int{}
	for _, group := range groups {
		if group.BankID != "" {
			t.Fatalf("aggregate grouping carried a bank: %+v", group)
		}
		aggregate[[2]string{group.OperationType, group.Status}] = group.Count
	}
	want := map[[2]string]int{
		{"retain", "pending"}:         2,
		{"consolidate", "processing"}: 1,
		{"unknown", "failed"}:         1,
	}
	for key, value := range want {
		if aggregate[key] != value {
			t.Fatalf("group %v = %d, want %d (all: %+v)", key, aggregate[key], value, aggregate)
		}
	}
	if len(aggregate) != len(want) {
		t.Fatalf("groups = %d, want %d: %+v", len(aggregate), len(want), aggregate)
	}

	perBank, err := store.CountAsyncOperations(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range perBank {
		if group.BankID == "" {
			t.Fatalf("per-bank grouping lost the bank: %+v", group)
		}
	}
}
