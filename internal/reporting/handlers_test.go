package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// bank is the identifier the fixture creates and every route is served under.
const bank = "b1"

// newServer builds the family's route table over a store seeded with one bank,
// one document and two memory units, translating path patterns into the path
// values the real router sets.
func newServer(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _ := newServerWithStore(t)
	return mux
}

// newServerWithStore is newServer, but also hands back the store so a case can
// seed data the fixture does not carry (a bank's tag vocabulary, for instance).
func newServerWithStore(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
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
	handlers := NewHandlers(store)
	handlers.BankLLMHealthEnabled = true
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
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
	return mux, store
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
	updated := send(mux, http.MethodPatch, base+"/memories/"+id, `{"text":"fresh"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update memory: %d, body = %s", updated.Code, updated.Body.String())
	}
	if rec := send(mux, http.MethodGet, base+"/memories/"+id+"/history", ""); rec.Code != http.StatusOK {
		t.Fatalf("memory history: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodDelete, base+"/memories/"+id+"/observations", ""); rec.Code != http.StatusOK {
		t.Fatalf("clear memory observations: %d", rec.Code)
	}
	if rec := send(mux, http.MethodPatch, base+"/memories/"+id, "not json"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed update: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/memories/does-not-exist", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown memory: %d, want 404", rec.Code)
	}
}

// The observation and consolidation routes answer through the operation queue.
func TestObservationAndConsolidationRoutes(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	if rec := send(mux, http.MethodPost, base+"/consolidation-strategies/preview", `{"strategies":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("consolidation preview: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodPost, base+"/consolidation/recover", ""); rec.Code != http.StatusOK {
		t.Fatalf("consolidation recover: %d", rec.Code)
	}
	consolidate := send(mux, http.MethodPost, base+"/consolidate", "")
	if consolidate.Code != http.StatusOK {
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
	folder := send(mux, http.MethodPost, base+"/knowledge-base/folders", `{"name":"Runbooks"}`)
	if folder.Code != http.StatusCreated {
		t.Fatalf("create folder: %d, body = %s", folder.Code, folder.Body.String())
	}
	page := send(mux, http.MethodPost, base+"/knowledge-base/pages", `{"name":"Deploy","content":"steps"}`)
	if page.Code != http.StatusCreated {
		t.Fatalf("create page: %d, body = %s", page.Code, page.Body.String())
	}
	var created struct {
		ID string `json:"page_id"`
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
	if rec := send(mux, http.MethodPost, base+"/memories/dry-run-extract", `{"content":"Alice works at Google."}`); rec.Code != http.StatusOK {
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

// The per-bank LLM health route reports not-found when the feature is off,
// matching the reference gate.
func TestHealthLLMGated(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/health/llm", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled llm health = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// The per-bank LLM health route reports per-operation status and latency, with
// no prober running every operation as not configured.
func TestHealthLLMShape(t *testing.T) {
	mux := newServer(t)
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/health/llm", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("llm health = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		BankID     string `json:"bank_id"`
		Operations []struct {
			Operation string   `json:"operation"`
			OK        bool     `json:"ok"`
			Status    string   `json:"status"`
			LatencyMS *float64 `json:"latency_ms"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Operations) != 3 {
		t.Fatalf("expected three operation probes, got %d", len(body.Operations))
	}
	for _, op := range body.Operations {
		if op.Status != "not_configured" || op.OK {
			t.Fatalf("a deployment with no prober must be not_configured: %+v", op)
		}
	}
}

// A second page of the same name in one folder is refused with a conflict.
func TestDuplicateKnowledgePageRejected(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	body := `{"name":"Deploy","content":"steps"}`
	if rec := send(mux, http.MethodPost, base+"/knowledge-base/pages", body); rec.Code != http.StatusCreated {
		t.Fatalf("first page: %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := send(mux, http.MethodPost, base+"/knowledge-base/pages", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate page = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("conflict detail = %s", rec.Body.String())
	}
}

// The prompt preview renders the real prompt of each operation rather than an
// empty stub, and reports the operations it can render.
func TestPromptPreviewRendersRealPrompts(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	for _, operation := range []string{"retain", "consolidation", "reflect"} {
		rec := send(mux, http.MethodPost, base+"/prompts/preview", `{"operation":"`+operation+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d, body = %s", operation, rec.Code, rec.Body.String())
		}
		var payload PromptPreviewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Messages) == 0 {
			t.Fatalf("%s: no messages rendered", operation)
		}
		if payload.SkippedReason != nil {
			t.Fatalf("%s: unexpectedly skipped: %v", operation, *payload.SkippedReason)
		}
		var sawSystem, sawUser bool
		for _, message := range payload.Messages {
			switch message.Role {
			case "system":
				sawSystem = true
			case "user":
				sawUser = true
			}
			if len(message.Blocks) == 0 {
				t.Fatalf("%s: a message carried no blocks", operation)
			}
			if strings.TrimSpace(message.Blocks[0].Text) == "" {
				t.Fatalf("%s: an empty block was rendered", operation)
			}
		}
		if !sawSystem || !sawUser {
			t.Fatalf("%s: expected both a system and a user message", operation)
		}
		if len(payload.Strategies) != 3 {
			t.Fatalf("%s: strategies = %v", operation, payload.Strategies)
		}
	}
	// The retain render carries the extraction schema, so a caller can see the
	// shape it will get back.
	rec := send(mux, http.MethodPost, base+"/prompts/preview", `{"operation":"retain"}`)
	var retained PromptPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &retained); err != nil {
		t.Fatal(err)
	}
	if retained.ResponseSchema == nil {
		t.Fatal("the retain preview must carry its response schema")
	}
}

// An unknown operation is refused rather than answered with an invented prompt.
func TestPromptPreviewRejectsUnknownOperation(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	rec := send(mux, http.MethodPost, base+"/prompts/preview", `{"operation":"summarise"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// The retain preview renders the bank's configured extraction mode, so a
// custom-mode bank sees its instructions and a bank that never selected custom
// mode does not — the preview tells the truth about what the retain will send.
func TestPromptPreviewRendersTheBanksExtractionMode(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	const instructions = "Record only the neighbourhood, not just the city."
	config := map[string]any{
		"retain_extraction_mode":     "custom",
		"retain_custom_instructions": instructions,
	}
	if _, err := store.UpdateBank(ctx, bank, storage.BankPatch{Config: &config}); err != nil {
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
			handler(w, r)
		})
	})
	rendered := func() string {
		rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/prompts/preview", `{"operation":"retain"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview: %d, body = %s", rec.Code, rec.Body.String())
		}
		var payload PromptPreviewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		var text strings.Builder
		for _, message := range payload.Messages {
			for _, block := range message.Blocks {
				text.WriteString(block.Text)
			}
		}
		return text.String()
	}
	if got := rendered(); !strings.Contains(got, instructions) {
		t.Fatalf("custom-mode preview is missing the bank's instructions:\n%s", got)
	}
	// Outside custom mode the stored instruction is inert, and the preview must
	// show it missing rather than pretend it is in force.
	config["retain_extraction_mode"] = "concise"
	if _, err := store.UpdateBank(ctx, bank, storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	if got := rendered(); strings.Contains(got, instructions) {
		t.Fatalf("concise-mode preview must not carry custom instructions:\n%s", got)
	}
}

// scopedHandler wraps the family's routes with the real tag-scope middleware,
// which resolves an API key through the reference's UserTagScope validator into
// the read scope the caller is confined to.
func scopedHandler(t *testing.T, store storage.Store) http.Handler {
	t.Helper()
	host := httpx.NewExtensionHost()
	host.RegisterOperationValidator(httpx.UserTagScopeValidator{})
	return httpx.OperationValidationMiddleware(host)(reportingMux(store))
}

// sendKeyed issues a request carrying an API key, so the middleware resolves the
// caller's tag scope from it.
func sendKeyed(handler http.Handler, apiKey, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// The memory listing is gated by the caller's resolved read scope: a scoped
// reader pages over only the memories its scope admits and the total agrees,
// while an unscoped caller sees every memory.
func TestMemoryListHonorsTagScope(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []storage.MemoryUnit{
		{ID: "m-kate", BankID: bank, Text: "Kate is interviewing at another company", Tags: []string{"user:kate"}},
		{ID: "m-dan", BankID: bank, Text: "Dan is planning a production deploy", Tags: []string{"user:dan"}},
		{ID: "m-rule", BankID: bank, Text: "Nobody deploys on Fridays", Tags: []string{"user:kate", "kind:rule"}},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	handler := scopedHandler(t, store)
	base := "/v1/default/banks/" + bank

	var admin MemoryListResponse
	if err := json.Unmarshal(sendKeyed(handler, "", http.MethodGet, base+"/memories/list").Body.Bytes(), &admin); err != nil {
		t.Fatal(err)
	}
	if admin.Total != 3 || len(admin.Items) != 3 {
		t.Fatalf("unscoped list = %d/%d, want 3/3", admin.Total, len(admin.Items))
	}

	var dan MemoryListResponse
	if err := json.Unmarshal(sendKeyed(handler, "dan", http.MethodGet, base+"/memories/list").Body.Bytes(), &dan); err != nil {
		t.Fatal(err)
	}
	for _, item := range dan.Items {
		if item.Text == "Kate is interviewing at another company" {
			t.Fatalf("dan list leaked kate's private memory: %+v", dan.Items)
		}
	}
	// Dan may read the shared rule and his own memory, so the scope leaves two.
	if dan.Total != 2 || len(dan.Items) != 2 {
		t.Fatalf("dan list = %d/%d, want 2/2", dan.Total, len(dan.Items))
	}
	if dan.Total != len(dan.Items) {
		t.Fatalf("dan total = %d, items = %d; they must agree", dan.Total, len(dan.Items))
	}
}
