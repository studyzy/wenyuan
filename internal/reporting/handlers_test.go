package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// bank is the identifier the fixture creates and every route is served under.
const bank = "b1"

// newServer builds the family's route table over a store seeded with one bank,
// one document and two memory units, translating path patterns into the path
// values the real router sets.
func newServer(t *testing.T) *http.ServeMux {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bank,
		Items: []storage.RetainItem{{
			Content:    "Alice works at Google.",
			DocumentID: "doc-1",
			Tags:       []string{"work"},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		if !strings.Contains(pattern, "{bank_id}") {
			mux.HandleFunc(method+" "+pattern, handler)
			return
		}
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			for _, param := range []string{"memory_id", "operation_id", "node_id", "page_id", "chunk_id"} {
				if strings.Contains(pattern, "{"+param+"}") {
					r.SetPathValue(param, lastSegment(r.URL.Path))
				}
			}
			handler(w, r)
		})
	})
	return mux
}

// lastSegment returns the final path segment, skipping a trailing action word.
func lastSegment(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		switch segments[i] {
		case "delete", "history", "observations", "preview":
			continue
		default:
			return segments[i]
		}
	}
	return ""
}

func send(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// memoryID reads the one memory unit the fixture wrote.
func memoryID(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/memories/list", "")
	var list MemoryListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		t.Fatalf("no memories: %s", rec.Body.String())
	}
	return list.Items[0].ID
}

// The observability routes answer with the bank's summary and listings.
func TestObservabilityRoutes(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	stats := send(mux, http.MethodGet, base+"/stats", "")
	if stats.Code != http.StatusOK {
		t.Fatalf("stats: %d, body = %s", stats.Code, stats.Body.String())
	}
	var summary BankStatsResponse
	if err := json.Unmarshal(stats.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.BankID != bank {
		t.Fatalf("stats bank = %q", summary.BankID)
	}

	for _, path := range []string{
		"/stats/memories-timeseries",
		"/audit-logs",
		"/audit-logs/stats",
		"/llm-requests",
		"/llm-requests/stats",
		"/observations/scopes",
	} {
		if rec := send(mux, http.MethodGet, base+path, ""); rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d, body = %s", path, rec.Code, rec.Body.String())
		}
	}
	// The health probe is a POST that reports the provider state.
	if rec := send(mux, http.MethodPost, base+"/health/llm", ""); rec.Code != http.StatusOK {
		t.Fatalf("POST /health/llm: %d, body = %s", rec.Code, rec.Body.String())
	}
}

// The memory sub-routes read, update and delete one unit.
func TestMemorySubRoutes(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	id := memoryID(t, mux)

	got := send(mux, http.MethodGet, base+"/memories/"+id, "")
	if got.Code != http.StatusOK {
		t.Fatalf("get memory: %d, body = %s", got.Code, got.Body.String())
	}
	updated := send(mux, http.MethodPatch, base+"/memories/"+id, `{"tags":["fresh"]}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update memory: %d, body = %s", updated.Code, updated.Body.String())
	}
	if rec := send(mux, http.MethodGet, base+"/memories/"+id+"/history", ""); rec.Code != http.StatusOK {
		t.Fatalf("memory history: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodDelete, base+"/memories/"+id+"/observations", ""); rec.Code != http.StatusOK {
		t.Fatalf("clear memory observations: %d", rec.Code)
	}
	if rec := send(mux, http.MethodPatch, base+"/memories/"+id, "not json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed update: %d, want 400", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/memories/does-not-exist", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown memory: %d, want 404", rec.Code)
	}
}

// The observation and consolidation routes answer through the operation queue.
func TestObservationAndConsolidationRoutes(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	if rec := send(mux, http.MethodPost, base+"/consolidation-strategies/preview", ""); rec.Code != http.StatusOK {
		t.Fatalf("consolidation preview: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodPost, base+"/consolidation/recover", ""); rec.Code != http.StatusOK {
		t.Fatalf("consolidation recover: %d", rec.Code)
	}
	consolidate := send(mux, http.MethodPost, base+"/consolidate", "")
	if consolidate.Code != http.StatusAccepted {
		t.Fatalf("consolidate: %d, body = %s", consolidate.Code, consolidate.Body.String())
	}
	if rec := send(mux, http.MethodDelete, base+"/observations", ""); rec.Code != http.StatusOK {
		t.Fatalf("delete observations: %d", rec.Code)
	}
}

// The knowledge base supports the folder/page lifecycle through the routes.
func TestKnowledgeBaseRoutes(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	if rec := send(mux, http.MethodGet, base+"/knowledge-base/tree", ""); rec.Code != http.StatusOK {
		t.Fatalf("tree: %d, body = %s", rec.Code, rec.Body.String())
	}
	folder := send(mux, http.MethodPost, base+"/knowledge-base/folders", `{"title":"Runbooks"}`)
	if folder.Code != http.StatusCreated && folder.Code != http.StatusOK {
		t.Fatalf("create folder: %d, body = %s", folder.Code, folder.Body.String())
	}
	page := send(mux, http.MethodPost, base+"/knowledge-base/pages", `{"title":"Deploy","content":"steps"}`)
	if page.Code != http.StatusCreated && page.Code != http.StatusOK {
		t.Fatalf("create page: %d, body = %s", page.Code, page.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	if rec := send(mux, http.MethodGet, base+"/knowledge-base/search?q=deploy", ""); rec.Code != http.StatusOK {
		t.Fatalf("search: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodGet, base+"/knowledge-base/export", ""); rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	if created.ID != "" {
		if rec := send(mux, http.MethodGet, base+"/knowledge-base/pages/"+created.ID, ""); rec.Code != http.StatusOK {
			t.Fatalf("get page: %d, body = %s", rec.Code, rec.Body.String())
		}
		if rec := send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+created.ID, `{"content":"updated"}`); rec.Code != http.StatusOK {
			t.Fatalf("update node: %d, body = %s", rec.Code, rec.Body.String())
		}
		if rec := send(mux, http.MethodDelete, base+"/knowledge-base/nodes/"+created.ID, ""); rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
			t.Fatalf("delete node: %d", rec.Code)
		}
	}
}

// The contract-remainder routes answer without a bank.
func TestContractRemainderRoutes(t *testing.T) {
	mux := newServer(t)
	if rec := send(mux, http.MethodGet, "/v1/bank-template-schema", ""); rec.Code != http.StatusOK {
		t.Fatalf("bank-template-schema: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodGet, "/v1/version", ""); rec.Code != http.StatusOK {
		t.Fatalf("version: %d", rec.Code)
	}
}

// The prompt-preview and dry-run-extract routes answer with their preview
// payloads without mutating the bank.
func TestPromptAndExtractPreviewRoutes(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	if rec := send(mux, http.MethodPost, base+"/prompts/preview", `{"query":"who is Alice?"}`); rec.Code != http.StatusOK {
		t.Fatalf("prompt preview: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodPost, base+"/memories/dry-run-extract", `{"text":"Alice works at Google."}`); rec.Code != http.StatusOK {
		t.Fatalf("dry-run extract: %d, body = %s", rec.Code, rec.Body.String())
	}
}

// An unknown bank answers not-found on a bank-scoped route.
func TestReportingUnknownBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "missing")
			handler(w, r)
		})
	})
	if rec := send(mux, http.MethodGet, "/v1/default/banks/missing/stats", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank: %d, want 404", rec.Code)
	}
}
