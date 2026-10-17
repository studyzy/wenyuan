package storage

import (
	"context"
	"testing"
	"time"
)

// The audit and LLM-request listings narrow by every documented filter. The
// fixture times are whole hours so a window edge is unambiguous; the base is
// today's UTC midnight so the rows also fall inside the statistics routes'
// bounded window (the reference filters on `started_at >= now - period`).
func observabilityBase() time.Time {
	return time.Now().UTC().Truncate(24 * time.Hour)
}

// seedObservabilityStore builds a bank with a few audit rows and model-call
// traces shaped so each filter selects a distinct subset.
func seedObservabilityStore(t *testing.T) (*MemoryStore, string) {
	t.Helper()
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	base := observabilityBase()
	store.mu.Lock()
	store.auditEntries = append(store.auditEntries,
		AuditEntry{ID: "a1", BankID: "b1", Action: "retain", Transport: "http", StartedAt: base},
		AuditEntry{ID: "a2", BankID: "b1", Action: "recall", Transport: "mcp", StartedAt: base.Add(time.Hour)},
		AuditEntry{ID: "a3", BankID: "b1", Action: "retain", Transport: "http", StartedAt: base.Add(2 * time.Hour)},
	)
	store.mu.Unlock()
	for _, request := range []LLMRequest{
		{ID: "r1", BankID: "b1", TraceID: "T1", Status: "success", Operation: "retain",
			Scope: "extraction", Provider: "openai", StartedAt: base,
			Metadata: map[string]any{"document_id": "doc-1", "memory_ids": []any{"m1"}}},
		{ID: "r2", BankID: "b1", TraceID: "T1", Status: "error", Operation: "retain",
			Scope: "extraction", Provider: "openai", StartedAt: base.Add(time.Hour),
			Metadata: map[string]any{"document_id": "doc-1", "source_memory_ids": []any{"m2"}}},
		{ID: "r3", BankID: "b1", TraceID: "T2", Status: "success", Operation: "reflect",
			Scope: "synthesis", Provider: "anthropic", StartedAt: base.Add(2 * time.Hour),
			Metadata: map[string]any{"document_id": "doc-2"}},
		{ID: "r4", BankID: "b1", Status: "success", Operation: "consolidation",
			StartedAt: base.Add(3 * time.Hour), Metadata: map[string]any{}},
	} {
		if err := store.RecordLLMRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	return store, "b1"
}

// idsOf maps a page of audit entries onto their identifiers, newest first.
func auditIDs(entries []AuditEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.ID)
	}
	return out
}

// requestIDs maps a page of traces onto their identifiers, newest first.
func requestIDs(requests []LLMRequest) []string {
	out := make([]string, 0, len(requests))
	for _, request := range requests {
		out = append(out, request.ID)
	}
	return out
}

func equalIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The audit listing honours action, transport and the half-open date window.
func TestMemoryAuditLogFilters(t *testing.T) {
	store, bank := seedObservabilityStore(t)
	ctx := context.Background()
	base := observabilityBase()
	list := func(query AuditQuery) []string {
		page, err := store.ListAuditLog(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		return auditIDs(page.Entries)
	}

	if got := list(AuditQuery{BankID: bank, Limit: 50}); !equalIDs(got, []string{"a3", "a2", "a1"}) {
		t.Fatalf("unfiltered = %v", got)
	}
	if got := list(AuditQuery{BankID: bank, Action: "retain", Limit: 50}); !equalIDs(got, []string{"a3", "a1"}) {
		t.Fatalf("action filter = %v", got)
	}
	if got := list(AuditQuery{BankID: bank, Transport: "mcp", Limit: 50}); !equalIDs(got, []string{"a2"}) {
		t.Fatalf("transport filter = %v", got)
	}
	// An unknown transport is not an error: it simply matches nothing.
	if got := list(AuditQuery{BankID: bank, Transport: "carrier-pigeon", Limit: 50}); len(got) != 0 {
		t.Fatalf("unknown transport = %v, want none", got)
	}
	// The window is inclusive at the start and exclusive at the end.
	start := base.Add(time.Hour)
	if got := list(AuditQuery{BankID: bank, StartDate: &start, Limit: 50}); !equalIDs(got, []string{"a3", "a2"}) {
		t.Fatalf("start_date (inclusive) = %v", got)
	}
	end := base.Add(2 * time.Hour)
	if got := list(AuditQuery{BankID: bank, EndDate: &end, Limit: 50}); !equalIDs(got, []string{"a2", "a1"}) {
		t.Fatalf("end_date (exclusive) = %v", got)
	}
	if got := list(AuditQuery{BankID: bank, StartDate: &start, EndDate: &end, Limit: 50}); !equalIDs(got, []string{"a2"}) {
		t.Fatalf("window = %v", got)
	}
	// A window combining an action keeps both conditions.
	if got := list(AuditQuery{BankID: bank, Action: "retain", StartDate: &start, Limit: 50}); !equalIDs(got, []string{"a3"}) {
		t.Fatalf("action + window = %v", got)
	}
}

// The audit histogram narrows to one action; an unknown one yields no buckets.
func TestMemoryAuditLogStatsFilter(t *testing.T) {
	store, bank := seedObservabilityStore(t)
	ctx := context.Background()

	all, err := store.AuditLogStats(ctx, bank, "day", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Bucket) != 1 || all.Bucket[0].Total != 3 {
		t.Fatalf("unfiltered buckets = %+v", all.Bucket)
	}
	narrowed, err := store.AuditLogStats(ctx, bank, "day", "retain")
	if err != nil {
		t.Fatal(err)
	}
	if len(narrowed.Bucket) != 1 || narrowed.Bucket[0].Total != 2 {
		t.Fatalf("action buckets = %+v", narrowed.Bucket)
	}
	if _, ok := narrowed.Bucket[0].Actions["recall"]; ok {
		t.Fatalf("action filter leaked a recall bucket: %+v", narrowed.Bucket[0].Actions)
	}
	unknown, err := store.AuditLogStats(ctx, bank, "day", "carrier-pigeon")
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown.Bucket) != 0 {
		t.Fatalf("unknown action buckets = %+v, want none", unknown.Bucket)
	}
}

// The model-request listing honours every filter the route exposes.
func TestMemoryLLMRequestFilters(t *testing.T) {
	store, bank := seedObservabilityStore(t)
	ctx := context.Background()
	base := observabilityBase()
	list := func(query LLMRequestQuery) []string {
		page, err := store.ListLLMRequests(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		return requestIDs(page.Requests)
	}

	if got := list(LLMRequestQuery{BankID: bank, Limit: 50}); !equalIDs(got, []string{"r4", "r3", "r2", "r1"}) {
		t.Fatalf("unfiltered = %v", got)
	}
	cases := []struct {
		name  string
		query LLMRequestQuery
		want  []string
	}{
		{"status", LLMRequestQuery{Status: "success"}, []string{"r4", "r3", "r1"}},
		{"operation", LLMRequestQuery{Operation: "retain"}, []string{"r2", "r1"}},
		{"scope", LLMRequestQuery{Scope: "synthesis"}, []string{"r3"}},
		{"provider", LLMRequestQuery{Provider: "anthropic"}, []string{"r3"}},
		{"trace", LLMRequestQuery{TraceID: "T1"}, []string{"r2", "r1"}},
		{"document", LLMRequestQuery{DocumentID: "doc-2", DocumentIDSet: true}, []string{"r3"}},
		{"empty document", LLMRequestQuery{DocumentID: "", DocumentIDSet: true}, nil},
		{"produced memory", LLMRequestQuery{MemoryID: "m1", MemoryIDSet: true}, []string{"r1"}},
		{"consumed memory", LLMRequestQuery{MemoryID: "m2", MemoryIDSet: true}, []string{"r2"}},
		{"absent memory", LLMRequestQuery{MemoryID: "m9", MemoryIDSet: true}, nil},
		{"unknown status", LLMRequestQuery{Status: "weird"}, nil},
		{"unknown operation", LLMRequestQuery{Operation: "frobnicate"}, nil},
	}
	for _, testCase := range cases {
		query := testCase.query
		query.BankID = bank
		query.Limit = 50
		if got := list(query); !equalIDs(got, testCase.want) {
			t.Fatalf("%s filter = %v, want %v", testCase.name, got, testCase.want)
		}
	}
	start := base.Add(time.Hour)
	if got := list(LLMRequestQuery{BankID: bank, StartDate: &start, Limit: 50}); !equalIDs(got, []string{"r4", "r3", "r2"}) {
		t.Fatalf("start_date (inclusive) = %v", got)
	}
	end := base.Add(2 * time.Hour)
	if got := list(LLMRequestQuery{BankID: bank, EndDate: &end, Limit: 50}); !equalIDs(got, []string{"r2", "r1"}) {
		t.Fatalf("end_date (exclusive) = %v", got)
	}
	if got := list(LLMRequestQuery{BankID: bank, Operation: "retain", Status: "error", Limit: 50}); !equalIDs(got, []string{"r2"}) {
		t.Fatalf("operation + status = %v", got)
	}
}

// Grouping pages whole runs: limit counts runs, and every row of a returned run
// comes back together.
func TestMemoryLLMRequestGrouping(t *testing.T) {
	store, bank := seedObservabilityStore(t)
	ctx := context.Background()

	page, err := store.ListLLMRequests(ctx, LLMRequestQuery{BankID: bank, Group: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Fatalf("run total = %d, want 3", page.Total)
	}
	// The most recent run is the untraced consolidation row.
	if got := requestIDs(page.Requests); !equalIDs(got, []string{"r4"}) {
		t.Fatalf("first run page = %v", got)
	}

	page, err = store.ListLLMRequests(ctx, LLMRequestQuery{BankID: bank, Group: true, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Fatalf("run total = %d, want 3", page.Total)
	}
	// The T2 run, then both rows of the T1 run, newest first.
	if got := requestIDs(page.Requests); !equalIDs(got, []string{"r3", "r2", "r1"}) {
		t.Fatalf("second run page = %v", got)
	}

	// The filter applies before grouping, so a trace filter keeps only its run.
	page, err = store.ListLLMRequests(ctx, LLMRequestQuery{BankID: bank, Group: true, TraceID: "T1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || !equalIDs(requestIDs(page.Requests), []string{"r2", "r1"}) {
		t.Fatalf("filtered group = total %d, %v", page.Total, requestIDs(page.Requests))
	}
}

// The model-request histogram narrows to one operation.
func TestMemoryLLMRequestStatsFilter(t *testing.T) {
	store, bank := seedObservabilityStore(t)
	ctx := context.Background()

	all, err := store.LLMRequestStats(ctx, bank, "day", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Bucket) != 1 || all.Bucket[0].Total != 4 {
		t.Fatalf("unfiltered buckets = %+v", all.Bucket)
	}
	narrowed, err := store.LLMRequestStats(ctx, bank, "day", "retain")
	if err != nil {
		t.Fatal(err)
	}
	if len(narrowed.Bucket) != 1 || narrowed.Bucket[0].Total != 2 {
		t.Fatalf("operation buckets = %+v", narrowed.Bucket)
	}
	// The two retain calls are one error and one success.
	if narrowed.Bucket[0].Statuses["error"] != 1 || narrowed.Bucket[0].Statuses["success"] != 1 {
		t.Fatalf("operation filter statuses = %+v", narrowed.Bucket[0].Statuses)
	}
	unknown, err := store.LLMRequestStats(ctx, bank, "day", "frobnicate")
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown.Bucket) != 0 {
		t.Fatalf("unknown operation buckets = %+v, want none", unknown.Bucket)
	}
}
