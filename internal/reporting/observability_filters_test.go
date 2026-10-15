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
	llmArgs   [2]string // period, operation
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

func (c *captureStore) LLMRequestStats(ctx context.Context, bankID, period, operation string) (storage.LLMRequestStats, error) {
	c.llmArgs = [2]string{period, operation}
	return c.Store.LLMRequestStats(ctx, bankID, period, operation)
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
	started := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, request := range []storage.LLMRequest{
		{ID: "t1", BankID: bank, Status: "success", Operation: "retain", StartedAt: started},
		{ID: "t2", BankID: bank, Status: "success", Operation: "reflect", StartedAt: started.Add(time.Hour)},
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
	if page.Total != 2 || len(page.Items) != 2 {
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
	if store.llmArgs != [2]string{"1d", "reflect"} {
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
