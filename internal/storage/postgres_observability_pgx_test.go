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
		{"document", 1, func() int { return count(LLMRequestQuery{BankID: "b1", DocumentID: "doc-2", Limit: 50}) }},
		{"produced memory", 1, func() int { return count(LLMRequestQuery{BankID: "b1", MemoryID: "m1", Limit: 50}) }},
		{"consumed memory", 1, func() int { return count(LLMRequestQuery{BankID: "b1", MemoryID: "m2", Limit: 50}) }},
		{"absent memory", 0, func() int { return count(LLMRequestQuery{BankID: "b1", MemoryID: "m9", Limit: 50}) }},
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
