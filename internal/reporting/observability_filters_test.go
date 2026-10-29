package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// captureStore records the query a handler hands the store, then delegates to
// the in-memory store underneath, so the route's parameter parsing can be
// asserted without a database.
type captureStore struct {
	storage.Store
	audit     storage.AuditQuery
	llm       storage.LLMRequestQuery
	auditArgs [2]string // period, action
	llmArgs   [3]string // period, operation, call type
}

func (c *captureStore) ListAuditLog(ctx context.Context, query storage.AuditQuery) (storage.AuditPage, error) {
	c.audit = query
	return c.Store.ListAuditLog(ctx, query)
}

func (c *captureStore) AuditLogStats(ctx context.Context, bankID, period, action string) (storage.AuditStats, error) {
	c.auditArgs = [2]string{period, action}
	return c.Store.AuditLogStats(ctx, bankID, period, action)
}

func (c *captureStore) ListLLMRequests(ctx context.Context, query storage.LLMRequestQuery) (storage.LLMRequestPage, error) {
	c.llm = query
	return c.Store.ListLLMRequests(ctx, query)
}

func (c *captureStore) LLMRequestStats(ctx context.Context, bankID, period, operation, callType string) (storage.LLMRequestStats, error) {
	c.llmArgs = [3]string{period, operation, callType}
	return c.Store.LLMRequestStats(ctx, bankID, period, operation, callType)
}

// newCaptureServer builds the route table over a capture store seeded with one
// bank and two model-call traces of different operations.
func newCaptureServer(t *testing.T) (*http.ServeMux, *captureStore) {
	t.Helper()
	memory := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := memory.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	// Today's UTC midnight keeps the rows inside the statistics routes' bounded
	// window (the reference filters on `started_at >= now - period`).
	started := time.Now().UTC().Truncate(24 * time.Hour)
	for _, request := range []storage.LLMRequest{
		{ID: "t1", BankID: bank, Status: "success", Operation: "retain", Scope: "extraction", StartedAt: started},
		{ID: "t2", BankID: bank, Status: "success", Operation: "reflect", Scope: "synthesis", StartedAt: started.Add(time.Hour)},
		{ID: "t3", BankID: bank, Status: "success", Operation: "recall", Scope: "embedding_query",
			StartedAt: started.Add(2 * time.Hour)},
		{ID: "t4", BankID: bank, Status: "success", Operation: "recall", Scope: "rerank",
			StartedAt: started.Add(3 * time.Hour)},
	} {
		if err := memory.RecordLLMRequest(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range []storage.AuditEntry{
		{ID: "a1", BankID: bank, Action: "retain", Transport: "http", StartedAt: started},
		{ID: "a2", BankID: bank, Action: "recall", Transport: "mcp", StartedAt: started.Add(time.Hour)},
		{ID: "a3", BankID: bank, Action: "retain", Transport: "http", StartedAt: started.Add(2 * time.Hour)},
	} {
		if err := memory.RecordAuditLog(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	store := &captureStore{Store: memory}
	mux := http.NewServeMux()
	handlers := NewHandlers(store)
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		if !strings.Contains(pattern, "{bank_id}") {
			mux.HandleFunc(method+" "+pattern, handler)
			return
		}
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			handler(w, r)
		})
	})
	return mux, store
}

// The audit listing forwards action, transport and the date window, and rejects
// a malformed date with a bad request.
func TestAuditLogsHandlerFilters(t *testing.T) {
	mux, store := newCaptureServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodGet, base+"/audit-logs?action=retain&transport=http"+
		"&start_date=2024-01-01T00:00:00Z&end_date=2024-01-02T00:00:00Z&limit=5&offset=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit-logs: %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.audit.Action != "retain" || store.audit.Transport != "http" {
		t.Fatalf("audit query = %+v", store.audit)
	}
	if store.audit.StartDate == nil || !store.audit.StartDate.Equal(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("start_date = %v", store.audit.StartDate)
	}
	if store.audit.EndDate == nil || !store.audit.EndDate.Equal(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("end_date = %v", store.audit.EndDate)
	}
	if store.audit.Limit != 5 || store.audit.Offset != 2 {
		t.Fatalf("paging = %d/%d", store.audit.Limit, store.audit.Offset)
	}

	// Absent filters select everything: with no query string the listing is the
	// whole trail, newest first.
	var page AuditLogListResponse
	rec = send(mux, http.MethodGet, base+"/audit-logs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit-logs (no filters): %d", rec.Code)
	}
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 3 || len(page.Items) != 3 || page.Items[0].ID != "a3" {
		t.Fatalf("unfiltered audit list = %+v", page)
	}

	// Combined filters narrow: action and transport both apply, so retain+http
	// keeps two rows and retain+mcp keeps none.
	rec = send(mux, http.MethodGet, base+"/audit-logs?action=retain&transport=http", "")
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("retain+http = %+v", page)
	}
	rec = send(mux, http.MethodGet, base+"/audit-logs?action=retain&transport=mcp", "")
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("retain+mcp = %+v", page)
	}

	if rec := send(mux, http.MethodGet, base+"/audit-logs?start_date=not-a-date", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed start_date: %d, want 400", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/audit-logs?end_date=2024-13-40", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed end_date: %d, want 400", rec.Code)
	}
}

// The model-request listing forwards every documented filter, end to end: an
// operation filter selects only that operation's traces.
func TestLLMRequestsHandlerFilters(t *testing.T) {
	mux, store := newCaptureServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodGet, base+"/llm-requests?status=success&operation=retain"+
		"&scope=extraction&provider=openai&trace_id=T1&document_id=doc-1&memory_id=m1&group=true"+
		"&start_date=2024-01-01T00:00:00Z&end_date=2024-01-02T00:00:00Z", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("llm-requests: %d, body = %s", rec.Code, rec.Body.String())
	}
	got := store.llm
	if got.Status != "success" || got.Operation != "retain" || got.Scope != "extraction" ||
		got.Provider != "openai" || got.TraceID != "T1" || got.DocumentID != "doc-1" || got.MemoryID != "m1" {
		t.Fatalf("llm query = %+v", got)
	}
	if !got.Group {
		t.Fatalf("group not parsed: %+v", got)
	}
	if got.StartDate == nil || got.EndDate == nil {
		t.Fatalf("dates not parsed: %+v", got)
	}

	// The filter reaches the store: only the retain trace comes back.
	rec = send(mux, http.MethodGet, base+"/llm-requests?operation=retain", "")
	var page LLMRequestListResponse
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "t1" {
		t.Fatalf("operation filter = %+v", page)
	}

	// An unknown enum value is not an error: it selects nothing.
	rec = send(mux, http.MethodGet, base+"/llm-requests?status=weird", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown status: %d, want 200", rec.Code)
	}
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("unknown status items = %+v", page)
	}

	// Absent filters select every trace; combined filters narrow.
	rec = send(mux, http.MethodGet, base+"/llm-requests", "")
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 4 || len(page.Items) != 4 {
		t.Fatalf("unfiltered llm list = %+v", page)
	}
	rec = send(mux, http.MethodGet, base+"/llm-requests?status=success&operation=retain", "")
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "t1" {
		t.Fatalf("success+retain = %+v", page)
	}

	if rec := send(mux, http.MethodGet, base+"/llm-requests?end_date=oops", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed end_date: %d, want 400", rec.Code)
	}
}

// The histogram routes forward their action and operation filters.
func TestObservabilityStatsHandlerFilters(t *testing.T) {
	mux, store := newCaptureServer(t)
	base := "/v1/default/banks/" + bank

	if rec := send(mux, http.MethodGet, base+"/audit-logs/stats?period=30d&action=retain", ""); rec.Code != http.StatusOK {
		t.Fatalf("audit stats: %d", rec.Code)
	}
	if store.auditArgs != [2]string{"30d", "retain"} {
		t.Fatalf("audit stats args = %v", store.auditArgs)
	}
	if rec := send(mux, http.MethodGet, base+"/llm-requests/stats?period=1d&operation=reflect", ""); rec.Code != http.StatusOK {
		t.Fatalf("llm stats: %d", rec.Code)
	}
	if store.llmArgs != [3]string{"1d", "reflect", ""} {
		t.Fatalf("llm stats args = %v", store.llmArgs)
	}
	// The operation filter narrows the histogram to reflect's single call.
	rec := send(mux, http.MethodGet, base+"/llm-requests/stats?operation=reflect", "")
	var stats LLMRequestStatsResponse
	jsonUnmarshal(t, rec.Body.Bytes(), &stats)
	if len(stats.Buckets) != 1 || stats.Buckets[0].Total != 1 {
		t.Fatalf("operation stats = %+v", stats.Buckets)
	}
}

// jsonUnmarshal decodes a response body into the target, failing the test on a
// decode error.
func jsonUnmarshal(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatal(err)
	}
}

// The model-request filters keep whitespace-only values and treat an explicitly
// empty document_id/memory_id as a real filter, matching the reference's truthy
// (`if value:`) and presence (`is not None`) tests.
func TestLLMRequestsFilterPresenceReachesStore(t *testing.T) {
	mux, store := newCaptureServer(t)
	base := "/v1/default/banks/" + bank

	// A whitespace-only status is a filter on the literal " ", not a dropped one.
	if rec := send(mux, http.MethodGet, base+"/llm-requests?status=%20", ""); rec.Code != http.StatusOK {
		t.Fatalf("whitespace status: %d", rec.Code)
	}
	if store.llm.Status != " " {
		t.Fatalf("whitespace status dropped: %q", store.llm.Status)
	}

	// An explicitly empty document_id is a filter that selects nothing.
	rec := send(mux, http.MethodGet, base+"/llm-requests?document_id=", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty document_id: %d", rec.Code)
	}
	if !store.llm.DocumentIDSet || store.llm.DocumentID != "" {
		t.Fatalf("empty document_id dropped: set=%v value=%q", store.llm.DocumentIDSet, store.llm.DocumentID)
	}
	var page LLMRequestListResponse
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("empty document_id selected %+v", page)
	}

	// Absent filters leave the presence flags off.
	if rec := send(mux, http.MethodGet, base+"/llm-requests", ""); rec.Code != http.StatusOK {
		t.Fatalf("no filters: %d", rec.Code)
	}
	if store.llm.DocumentIDSet || store.llm.MemoryIDSet {
		t.Fatalf("absent filters marked present: %+v", store.llm)
	}
}

// A whitespace-only audit action is a real filter, so it selects no row rather
// than being trimmed away.
func TestAuditLogsActionWhitespaceIsFilter(t *testing.T) {
	mux, store := newCaptureServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodGet, base+"/audit-logs?action=%20", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("whitespace action: %d", rec.Code)
	}
	if store.audit.Action != " " {
		t.Fatalf("whitespace action dropped: %q", store.audit.Action)
	}
	var page AuditLogListResponse
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("whitespace action selected %+v", page)
	}
}

// The model-request routes accept call_type and reject a value outside its
// enum with the framework's 422, before the bank is looked up.
func TestLLMRequestsCallTypeParam(t *testing.T) {
	mux, store := newCaptureServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodGet, base+"/llm-requests?call_type=embedding", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("call_type=embedding: %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.llm.CallType != "embedding" {
		t.Fatalf("call_type did not reach the store: %+v", store.llm)
	}
	var page LLMRequestListResponse
	jsonUnmarshal(t, rec.Body.Bytes(), &page)
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "t3" {
		t.Fatalf("embedding page = %+v", page)
	}

	rec = send(mux, http.MethodGet, base+"/llm-requests/stats?call_type=rerank", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats call_type=rerank: %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.llmArgs[2] != "rerank" {
		t.Fatalf("stats call_type did not reach the store: %+v", store.llmArgs)
	}

	// An unknown kind is a framework enum failure, not an empty page — and it
	// outranks the bank lookup, as the framework's own validation does.
	for _, path := range []string{base + "/llm-requests?call_type=chat",
		base + "/llm-requests/stats?call_type=chat",
		"/v1/default/banks/no-such-bank/llm-requests?call_type=chat"} {
		rec = send(mux, http.MethodGet, path, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d, want 422", path, rec.Code)
		}
		var envelope struct {
			Detail []struct {
				Type  string `json:"type"`
				Loc   []any  `json:"loc"`
				Msg   string `json:"msg"`
				Input string `json:"input"`
			} `json:"detail"`
		}
		jsonUnmarshal(t, rec.Body.Bytes(), &envelope)
		if len(envelope.Detail) != 1 || envelope.Detail[0].Type != "literal_error" ||
			envelope.Detail[0].Input != "chat" || len(envelope.Detail[0].Loc) != 2 ||
			envelope.Detail[0].Loc[0] != "query" || envelope.Detail[0].Loc[1] != "call_type" {
			t.Fatalf("%s: body = %s", path, rec.Body.String())
		}
	}
}

// The audit listing serves summaries — no request/response bodies — and the new
// member route serves one entry with its bodies; an unknown or foreign entry is
// the reference's 404 detail.
func TestAuditLogSummaryAndMember(t *testing.T) {
	mux, store := newCaptureServer(t)
	base := "/v1/default/banks/" + bank
	started := time.Now().UTC().Truncate(24 * time.Hour)
	ended := started.Add(1500 * time.Millisecond)
	if err := store.RecordAuditLog(context.Background(), storage.AuditEntry{
		ID: "3f2504e0-4f89-11d3-9a0c-0305e82c3301", BankID: bank, Action: "retain", Transport: "http",
		StartedAt: started, EndedAt: &ended,
		Request:  map[string]any{"items": []any{}},
		Response: map[string]any{"status": "ok"},
	}); err != nil {
		t.Fatal(err)
	}

	rec := send(mux, http.MethodGet, base+"/audit-logs?limit=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit-logs: %d, body = %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Items []map[string]any `json:"items"`
	}
	jsonUnmarshal(t, rec.Body.Bytes(), &listed)
	if len(listed.Items) != 1 {
		t.Fatalf("items = %s", rec.Body.String())
	}
	for _, member := range []string{"id", "action", "transport", "bank_id", "started_at", "ended_at",
		"duration_ms", "metadata"} {
		if _, ok := listed.Items[0][member]; !ok {
			t.Fatalf("summary is missing %q: %s", member, rec.Body.String())
		}
	}
	for _, member := range []string{"request", "response"} {
		if _, ok := listed.Items[0][member]; ok {
			t.Fatalf("summary must not carry %q: %s", member, rec.Body.String())
		}
	}

	rec = send(mux, http.MethodGet, base+"/audit-logs/3f2504e0-4f89-11d3-9a0c-0305e82c3301", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit-log member: %d, body = %s", rec.Code, rec.Body.String())
	}
	var entry map[string]any
	jsonUnmarshal(t, rec.Body.Bytes(), &entry)
	if entry["action"] != "retain" || entry["duration_ms"] != float64(1500) {
		t.Fatalf("member = %s", rec.Body.String())
	}
	if request, ok := entry["request"].(map[string]any); !ok || request["items"] == nil {
		t.Fatalf("member request body = %s", rec.Body.String())
	}
	if response, ok := entry["response"].(map[string]any); !ok || response["status"] != "ok" {
		t.Fatalf("member response body = %s", rec.Body.String())
	}
	if _, ok := entry["metadata"].(map[string]any); !ok {
		t.Fatalf("member metadata = %s", rec.Body.String())
	}

	// An entry id nobody holds, and one that is not an identifier at all, are
	// the same 404 with the reference's detail.
	for _, id := range []string{"e3b0c442-98fc-1c14-9afb-f4c8996fb924", "not-a-uuid"} {
		rec = send(mux, http.MethodGet, base+"/audit-logs/"+id, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("audit-log %s: %d, want 404", id, rec.Code)
		}
		var envelope struct {
			Detail string `json:"detail"`
		}
		jsonUnmarshal(t, rec.Body.Bytes(), &envelope)
		if envelope.Detail != "Audit log entry '"+id+"' not found" {
			t.Fatalf("audit-log %s detail = %q", id, envelope.Detail)
		}
	}
}

// The entry fetch is bank-scoped: another bank's entry is not found, and an
// unknown bank answers the ordinary missing-bank 404.
func TestAuditLogMemberIsBankScoped(t *testing.T) {
	mux, store := newCaptureServer(t)
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAuditLog(ctx, storage.AuditEntry{
		ID: "3f2504e0-4f89-11d3-9a0c-0305e82c3302", BankID: "other", Action: "retain",
		Transport: "http", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	rec := send(mux, http.MethodGet,
		"/v1/default/banks/"+bank+"/audit-logs/3f2504e0-4f89-11d3-9a0c-0305e82c3302", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("foreign entry: %d, want 404", rec.Code)
	}
	rec = send(mux, http.MethodGet,
		"/v1/default/banks/no-such-bank/audit-logs/3f2504e0-4f89-11d3-9a0c-0305e82c3302", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank: %d, want 404", rec.Code)
	}
}
