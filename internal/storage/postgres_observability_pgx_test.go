//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
)

// newPostgresObservabilityStore opens a fresh schema with the reference tables
// and seeds one bank with the same audit and trace rows the in-memory filter
// test uses, so the two backends answer identically.
func newPostgresObservabilityStore(t *testing.T) (*PostgresStore, string, time.Time) {
	t.Helper()
	pg, schema := newClearMemoriesStore(t, "obs_filters")
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	base := observabilityBase()
	for _, entry := range []struct {
		action, transport string
		at                time.Time
	}{
		{"retain", "http", base},
		{"recall", "mcp", base.Add(time.Hour)},
		{"retain", "http", base.Add(2 * time.Hour)},
	} {
		if _, err := pg.db.ExecContext(ctx,
			`INSERT INTO `+quoteIdent(schema)+`.audit_log (id, action, transport, bank_id, started_at)
			 VALUES ($1, $2, $3, 'b1', $4)`,
			newID(), entry.action, entry.transport, entry.at); err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range []LLMRequest{
		{ID: newID(), BankID: "b1", TraceID: "T1", Status: "success", Operation: "retain",
			Scope: "extraction", Provider: "openai", StartedAt: base,
			Metadata: map[string]any{"document_id": "doc-1", "memory_ids": []string{"m1"}}},
		{ID: newID(), BankID: "b1", TraceID: "T1", Status: "error", Operation: "retain",
			Scope: "extraction", Provider: "openai", StartedAt: base.Add(time.Hour),
			Metadata: map[string]any{"document_id": "doc-1", "source_memory_ids": []string{"m2"}}},
		{ID: newID(), BankID: "b1", TraceID: "T2", Status: "success", Operation: "reflect",
			Scope: "synthesis", Provider: "anthropic", StartedAt: base.Add(2 * time.Hour),
			Metadata: map[string]any{"document_id": "doc-2"}},
		{ID: newID(), BankID: "b1", Status: "success", Operation: "consolidation",
			StartedAt: base.Add(3 * time.Hour), Metadata: map[string]any{}},
	} {
		if err := pg.RecordLLMRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	return pg, schema, base
}

// The PostgreSQL backend narrows the audit listing with the same filters.
func TestPostgresAuditLogFilters(t *testing.T) {
	pg, _, base := newPostgresObservabilityStore(t)
	ctx := context.Background()
	list := func(query AuditQuery) []string {
		page, err := pg.ListAuditLog(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		return auditIDs(page.Entries)
	}
	// Identifiers are generated, so the ordering is asserted through the action
	// and window rather than by fixed ids.
	if got := list(AuditQuery{BankID: "b1", Limit: 50}); len(got) != 3 {
		t.Fatalf("unfiltered = %v", got)
	}
	if got := list(AuditQuery{BankID: "b1", Action: "retain", Limit: 50}); len(got) != 2 {
		t.Fatalf("action filter = %v", got)
	}
	if got := list(AuditQuery{BankID: "b1", Transport: "mcp", Limit: 50}); len(got) != 1 {
		t.Fatalf("transport filter = %v", got)
	}
	if got := list(AuditQuery{BankID: "b1", Transport: "carrier-pigeon", Limit: 50}); len(got) != 0 {
		t.Fatalf("unknown transport = %v, want none", got)
	}
	start := base.Add(time.Hour)
	if got := list(AuditQuery{BankID: "b1", StartDate: &start, Limit: 50}); len(got) != 2 {
		t.Fatalf("start_date (inclusive) = %v", got)
	}
	end := base.Add(2 * time.Hour)
	if got := list(AuditQuery{BankID: "b1", EndDate: &end, Limit: 50}); len(got) != 2 {
		t.Fatalf("end_date (exclusive) = %v", got)
	}
	if got := list(AuditQuery{BankID: "b1", StartDate: &start, EndDate: &end, Limit: 50}); len(got) != 1 {
		t.Fatalf("window = %v", got)
	}
	if got := list(AuditQuery{BankID: "b1", Action: "retain", StartDate: &start, Limit: 50}); len(got) != 1 {
		t.Fatalf("action + window = %v", got)
	}
}

// The PostgreSQL backend narrows the model-request listing with every filter.
func TestPostgresLLMRequestFilters(t *testing.T) {
	pg, _, base := newPostgresObservabilityStore(t)
	ctx := context.Background()
	count := func(query LLMRequestQuery) int {
		page, err := pg.ListLLMRequests(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Requests)
	}
	if got := count(LLMRequestQuery{BankID: "b1", Limit: 50}); got != 4 {
		t.Fatalf("unfiltered = %d", got)
	}
	cases := []struct {
		name string
		want int
		list func() int
	}{
		{"status", 3, func() int { return count(LLMRequestQuery{BankID: "b1", Status: "success", Limit: 50}) }},
		{"operation", 2, func() int { return count(LLMRequestQuery{BankID: "b1", Operation: "retain", Limit: 50}) }},
		{"scope", 1, func() int { return count(LLMRequestQuery{BankID: "b1", Scope: "synthesis", Limit: 50}) }},
		{"provider", 1, func() int { return count(LLMRequestQuery{BankID: "b1", Provider: "anthropic", Limit: 50}) }},
		{"trace", 2, func() int { return count(LLMRequestQuery{BankID: "b1", TraceID: "T1", Limit: 50}) }},
		{"document", 1, func() int {
			return count(LLMRequestQuery{BankID: "b1", DocumentID: "doc-2", DocumentIDSet: true, Limit: 50})
		}},
		{"empty document", 0, func() int {
			return count(LLMRequestQuery{BankID: "b1", DocumentID: "", DocumentIDSet: true, Limit: 50})
		}},
		{"produced memory", 1, func() int { return count(LLMRequestQuery{BankID: "b1", MemoryID: "m1", MemoryIDSet: true, Limit: 50}) }},
		{"consumed memory", 1, func() int { return count(LLMRequestQuery{BankID: "b1", MemoryID: "m2", MemoryIDSet: true, Limit: 50}) }},
		{"absent memory", 0, func() int { return count(LLMRequestQuery{BankID: "b1", MemoryID: "m9", MemoryIDSet: true, Limit: 50}) }},
		{"unknown status", 0, func() int { return count(LLMRequestQuery{BankID: "b1", Status: "weird", Limit: 50}) }},
		{"unknown operation", 0, func() int { return count(LLMRequestQuery{BankID: "b1", Operation: "frobnicate", Limit: 50}) }},
	}
	for _, testCase := range cases {
		if got := testCase.list(); got != testCase.want {
			t.Fatalf("%s filter = %d, want %d", testCase.name, got, testCase.want)
		}
	}
	start := base.Add(time.Hour)
	if got := count(LLMRequestQuery{BankID: "b1", StartDate: &start, Limit: 50}); got != 3 {
		t.Fatalf("start_date (inclusive) = %d", got)
	}
	end := base.Add(2 * time.Hour)
	if got := count(LLMRequestQuery{BankID: "b1", EndDate: &end, Limit: 50}); got != 2 {
		t.Fatalf("end_date (exclusive) = %d", got)
	}
	if got := count(LLMRequestQuery{BankID: "b1", Operation: "retain", Status: "error", Limit: 50}); got != 1 {
		t.Fatalf("operation + status = %d", got)
	}
}

// The PostgreSQL backend pages whole runs when grouping.
func TestPostgresLLMRequestGrouping(t *testing.T) {
	pg, _, _ := newPostgresObservabilityStore(t)
	ctx := context.Background()

	page, err := pg.ListLLMRequests(ctx, LLMRequestQuery{BankID: "b1", Group: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Requests) != 1 {
		t.Fatalf("first run page = total %d, %d rows", page.Total, len(page.Requests))
	}
	if page.Requests[0].Operation != "consolidation" {
		t.Fatalf("first run op = %q", page.Requests[0].Operation)
	}

	page, err = pg.ListLLMRequests(ctx, LLMRequestQuery{BankID: "b1", Group: true, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Requests) != 3 {
		t.Fatalf("second run page = total %d, %d rows", page.Total, len(page.Requests))
	}
	// The whole T1 run comes back together, after the T2 row.
	if page.Requests[0].TraceID != "T2" || page.Requests[1].TraceID != "T1" || page.Requests[2].TraceID != "T1" {
		t.Fatalf("second run page traces = %q %q %q",
			page.Requests[0].TraceID, page.Requests[1].TraceID, page.Requests[2].TraceID)
	}

	page, err = pg.ListLLMRequests(ctx, LLMRequestQuery{BankID: "b1", Group: true, TraceID: "T1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Requests) != 2 {
		t.Fatalf("filtered group = total %d, %d rows", page.Total, len(page.Requests))
	}
}

// The PostgreSQL histograms narrow to one action and one operation.
func TestPostgresObservabilityStatsFilters(t *testing.T) {
	pg, _, _ := newPostgresObservabilityStore(t)
	ctx := context.Background()

	audit, err := pg.AuditLogStats(ctx, "b1", "day", "retain")
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Bucket) != 1 || audit.Bucket[0].Total != 2 {
		t.Fatalf("audit action buckets = %+v", audit.Bucket)
	}
	unknownAudit, err := pg.AuditLogStats(ctx, "b1", "day", "carrier-pigeon")
	if err != nil {
		t.Fatal(err)
	}
	if len(unknownAudit.Bucket) != 0 {
		t.Fatalf("unknown audit action buckets = %+v", unknownAudit.Bucket)
	}

	requests, err := pg.LLMRequestStats(ctx, "b1", "day", "retain")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests.Bucket) != 1 || requests.Bucket[0].Total != 2 {
		t.Fatalf("llm operation buckets = %+v", requests.Bucket)
	}
	unknownRequests, err := pg.LLMRequestStats(ctx, "b1", "day", "frobnicate")
	if err != nil {
		t.Fatal(err)
	}
	if len(unknownRequests.Bucket) != 0 {
		t.Fatalf("unknown llm operation buckets = %+v", unknownRequests.Bucket)
	}
}

// The postgres bank summary derives entity links from unit_entities membership
// (capped per entity), sums total_links over every category, leaves the
// breakdown fields as non-nil empty objects, and reads the write/consolidation
// watermarks off memory_units rather than the operation log.
func TestPostgresBankStatsLinksAndWatermarks(t *testing.T) {
	pg, schema := newClearMemoriesStore(t, "stats_links")
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	q := quoteIdent(schema)
	write := time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC)
	consolidated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entity := newID()
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+q+`.entities (id, bank_id, canonical_name) VALUES ($1, 'b1', 'E1')`, entity); err != nil {
		t.Fatal(err)
	}
	ids := []string{newID(), newID(), newID()}
	for i, id := range ids {
		var consolidatedAt any
		if i == 0 {
			consolidatedAt = consolidated
		}
		if _, err := pg.db.ExecContext(ctx,
			`INSERT INTO `+q+`.memory_units (id, bank_id, text, fact_type, updated_at, consolidated_at)
			 VALUES ($1, 'b1', 'fact', 'world', $2, $3)`, id, write, consolidatedAt); err != nil {
			t.Fatal(err)
		}
		if _, err := pg.db.ExecContext(ctx,
			`INSERT INTO `+q+`.unit_entities (unit_id, entity_id) VALUES ($1, $2)`, id, entity); err != nil {
			t.Fatal(err)
		}
	}
	// An observation unit, outside the consolidator's candidate set.
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+q+`.memory_units (id, bank_id, text, fact_type, updated_at)
		 VALUES ($1, 'b1', 'fact', 'observation', $2)`, newID(), write); err != nil {
		t.Fatal(err)
	}
	// One semantic link, so total_links = semantic 1 + entity LEAST(3-1,10) 2.
	if _, err := pg.db.ExecContext(ctx,
		`INSERT INTO `+q+`.memory_links (bank_id, from_unit_id, to_unit_id, link_type)
		 VALUES ('b1', $1, $2, 'semantic')`, ids[0], ids[1]); err != nil {
		t.Fatal(err)
	}

	stats, err := pg.BankStatsRefreshing(ctx, "b1", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := stats.LinksByLinkType[string(LinkEntity)]; got != 2 {
		t.Errorf("entity links = %d, want 2", got)
	}
	if got := stats.LinksByLinkType[string(LinkSemantic)]; got != 1 {
		t.Errorf("semantic links = %d, want 1", got)
	}
	if stats.TotalLinks != 3 {
		t.Errorf("total links = %d, want 3", stats.TotalLinks)
	}
	if stats.LinksByFactType == nil || len(stats.LinksByFactType) != 0 {
		t.Errorf("links_by_fact_type must be a non-nil empty map, got %v", stats.LinksByFactType)
	}
	if stats.LinksBreakdown == nil || len(stats.LinksBreakdown) != 0 {
		t.Errorf("links_breakdown must be a non-nil empty map, got %v", stats.LinksBreakdown)
	}
	// Two of the three world facts are unconsolidated.
	if stats.PendingConsolidation != 2 {
		t.Errorf("pending consolidation = %d, want 2", stats.PendingConsolidation)
	}
	if stats.FailedConsolidation != 0 {
		t.Errorf("failed consolidation = %d, want 0", stats.FailedConsolidation)
	}
	if stats.LastMemoryWriteAt == nil || !stats.LastMemoryWriteAt.UTC().Equal(write) {
		t.Errorf("last memory write = %v, want %v", stats.LastMemoryWriteAt, write)
	}
	if stats.LastConsolidatedAt == nil || !stats.LastConsolidatedAt.UTC().Equal(consolidated) {
		t.Errorf("last consolidated = %v, want %v", stats.LastConsolidatedAt, consolidated)
	}
	if stats.TotalObservations != 1 {
		t.Errorf("total observations = %d, want 1", stats.TotalObservations)
	}
}

// The PostgreSQL backend groups non-terminal operations by type and status the
// same way the in-memory backend does, excluding terminal states and carrying
// bank_id only for a per-bank breakdown.
func TestPostgresCountAsyncOperations(t *testing.T) {
	pg, _ := newClearMemoriesStore(t, "obs_async_ops")
	ctx := context.Background()
	for _, bank := range []string{"b1", "b2"} {
		if _, err := pg.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
			t.Fatal(err)
		}
	}
	seed := []Operation{
		{OperationID: newID(), BankID: "b1", OperationType: "retain", Status: StatusPending},
		{OperationID: newID(), BankID: "b1", OperationType: "retain", Status: StatusPending},
		{OperationID: newID(), BankID: "b1", OperationType: "consolidate", Status: StatusProcessing},
		{OperationID: newID(), BankID: "b2", OperationType: "retain", Status: StatusFailed},
		{OperationID: newID(), BankID: "b1", OperationType: "retain", Status: StatusCompleted},
	}
	for _, op := range seed {
		if _, err := pg.PutOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}

	groups, err := pg.CountAsyncOperations(ctx, false)
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
		{"retain", "failed"}:          1,
	}
	for key, value := range want {
		if aggregate[key] != value {
			t.Fatalf("group %v = %d, want %d (all: %+v)", key, aggregate[key], value, aggregate)
		}
	}
	if len(aggregate) != len(want) {
		t.Fatalf("groups = %d, want %d: %+v", len(aggregate), len(want), aggregate)
	}

	perBank, err := pg.CountAsyncOperations(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range perBank {
		if group.BankID == "" {
			t.Fatalf("per-bank grouping lost the bank: %+v", group)
		}
	}
}
