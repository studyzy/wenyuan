package reporting

import (
	"context"
	"encoding/json"
	"errors"
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
	// The breakdown fields stay in the shape but are deliberately empty objects
	// (never null), as the reference guarantees.
	body := stats.Body.String()
	for _, want := range []string{`"links_by_fact_type":{}`, `"links_breakdown":{}`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stats body missing %s: %s", want, body)
		}
	}
	// refresh=true is accepted and answered.
	if rec := send(mux, http.MethodGet, base+"/stats?refresh=true", ""); rec.Code != http.StatusOK {
		t.Fatalf("stats refresh: %d, body = %s", rec.Code, rec.Body.String())
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
	if rec := send(mux, http.MethodGet, base+"/memories/00000000-0000-0000-0000-000000000000", ""); rec.Code != http.StatusNotFound {
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
	page := send(mux, http.MethodPost, base+"/knowledge-base/pages", `{"name":"Deploy","source_query":"steps"}`)
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
		if rec := send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+created.ID, `{"name":"Deployed"}`); rec.Code != http.StatusOK {
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
	body := `{"name":"Deploy","source_query":"steps"}`
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
// empty stub, partitioned into per-setting blocks, and reports the bank's
// configured strategies (none, for an unconfigured bank).
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
			// The active blocks concatenate back to the message that is sent, so
			// at least one must be active.
			active := false
			for _, block := range message.Blocks {
				if block.Active {
					active = true
				}
				// No block may be a bare copy of the whole message: the point of
				// partitioning is that each names its setting in its field/heading.
				if block.Text == "" && block.Field == "" && block.Section == "" && block.Heading == "" {
					t.Fatalf("%s: an unattributed empty block was rendered", operation)
				}
			}
			if !active {
				t.Fatalf("%s: no active block in the %s message", operation, message.Role)
			}
		}
		if !sawSystem || !sawUser {
			t.Fatalf("%s: expected both a system and a user message", operation)
		}
		if len(payload.Strategies) != 0 {
			t.Fatalf("%s: strategies = %v, want none for an unconfigured bank", operation, payload.Strategies)
		}
	}
	// The retain render carries the extraction schema; consolidation sends none.
	rec := send(mux, http.MethodPost, base+"/prompts/preview", `{"operation":"retain"}`)
	var retained PromptPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &retained); err != nil {
		t.Fatal(err)
	}
	if retained.ResponseSchema == nil {
		t.Fatal("the retain preview must carry its response schema")
	}
	if len(retained.RunSettings) == 0 {
		t.Fatal("the retain preview must report its chunk-size run settings")
	}
	rec = send(mux, http.MethodPost, base+"/prompts/preview", `{"operation":"consolidation"}`)
	var consolidated PromptPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &consolidated); err != nil {
		t.Fatal(err)
	}
	if consolidated.ResponseSchema != nil {
		t.Fatalf("consolidation preview schema = %v, want null", consolidated.ResponseSchema)
	}
}

// An unknown operation is refused with the framework's Literal rejection rather
// than answered with an invented prompt.
func TestPromptPreviewRejectsUnknownOperation(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	rec := send(mux, http.MethodPost, base+"/prompts/preview", `{"operation":"summarise"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "literal_error") {
		t.Fatalf("body = %s, want a literal_error", rec.Body.String())
	}
}

// The preview route is the one bank-scoped route that does NOT catch the
// reference's OperationValidationError: an unknown bank escapes to the generic
// handler, which since #5448 maps the validator's own error to its carried
// status and reason — the same 404 a catching route answers.
func TestPromptPreviewUnknownBankAnswersUncaughtValidationError(t *testing.T) {
	store := storage.NewMemoryStore("public")
	const missing = "no-such-bank"
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", missing)
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+missing+"/prompts/preview", `{"operation":"retain"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	want := `{"detail":"Bank 'no-such-bank' not found"}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("body = %s, want %s", got, want)
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

// sendKeyedBody is sendKeyed with a JSON body.
func sendKeyedBody(handler http.Handler, apiKey, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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

// The reference keeps the dry-run route registered and answers 404 with its own
// sentence when the feature is switched off, so a client that probes the route
// learns which key turns it back on.
func TestDryRunExtractDisabledAnswersTheReferenceNotFound(t *testing.T) {
	// The route table is built here with the gate flipped: the reference keeps
	// the route registered and refuses it, rather than unregistering it.
	gated := http.NewServeMux()
	handlers := NewHandlers(storage.NewMemoryStore("public"))
	handlers.DryRunExtractDisabled = true
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		if !strings.Contains(pattern, "{bank_id}") {
			gated.HandleFunc(method+" "+pattern, handler)
			return
		}
		gated.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", bank)
			handler(w, r)
		})
	})

	rec := send(gated, http.MethodPost, "/v1/default/banks/"+bank+"/memories/dry-run-extract", `{"content":"Alice moved"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "WENYUAN_API_ENABLE_DRY_RUN_EXTRACT=true") {
		t.Fatalf("the refusal does not name the key that re-enables it: %s", rec.Body.String())
	}
}

// The knowledge-page default trigger layers the deployment's process default,
// the tenant override and the bank's own config, in that order, under whatever
// the create request named.
func TestKnowledgePageTriggerLayersProcessTenantAndBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	handlers := NewHandlers(store)
	handlers.ProcessDefaults = map[string]any{
		"knowledge_page_default_trigger": map[string]any{"refresh_cron": "0 * * * *"},
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/"+bank+"/knowledge-base/pages", nil)

	trigger, failure := handlers.knowledgePageTrigger(context.Background(), req, bank, nil)
	if failure != nil {
		t.Fatalf("resolution failed: %+v", failure)
	}
	if trigger["refresh_cron"] != "0 * * * *" {
		t.Fatalf("process default not applied: %+v", trigger)
	}
	// The built-in default's members survive underneath it.
	if trigger["mode"] != "delta" {
		t.Fatalf("the built-in page default was lost: %+v", trigger)
	}

	// The request's own trigger wins over every layer.
	request, failure := handlers.knowledgePageTrigger(context.Background(), req, bank,
		map[string]any{"refresh_cron": "*/5 * * * *"})
	if failure != nil {
		t.Fatalf("resolution failed: %+v", failure)
	}
	if request["refresh_cron"] != "*/5 * * * *" {
		t.Fatalf("the request's trigger did not win: %+v", request)
	}
}

// asHTTPError passes an error that already carries a status through untouched,
// names the storage sentinels a route can hit, and treats anything it does not
// name as a server fault.
func TestReportingErrorMapping(t *testing.T) {
	conflict := httpx.NewHTTPError(httpx.KindConflict, "A page named 'x' already exists in this folder")
	cases := []struct {
		err    error
		expect int
	}{
		{conflict, http.StatusConflict},
		{storage.ErrBankNotFound, http.StatusNotFound},
		{storage.ErrMemoryNotFound, http.StatusNotFound},
		{storage.ErrKnowledgeNodeNotFound, http.StatusNotFound},
		{storage.ErrAuditEntryNotFound, http.StatusNotFound},
		{storage.ErrMentalModelNotFound, http.StatusNotFound},
		{storage.ErrInvalidMentalModelSubtype, http.StatusBadRequest},
		{storage.ErrEmbeddingDimensions, http.StatusBadRequest},
		{storage.ErrInvalidBankID, http.StatusBadRequest},
		{storage.ErrAliasConflict, http.StatusConflict},
		{storage.ErrWritesUnavailable, http.StatusServiceUnavailable},
		{errors.New("storage exploded"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := asHTTPError(tc.err); got == nil || got.Status != tc.expect {
			t.Fatalf("error %v mapped to %+v, want status %d", tc.err, got, tc.expect)
		}
	}
	if asHTTPError(nil) != nil {
		t.Fatal("a nil error must map to nil")
	}
}

// The retain preview resolves the effective strategy (the caller's, else the
// bank's default), reports the bank's configured strategy names, and skips the
// render entirely for a bank whose mode stores chunks verbatim.
func TestPromptPreviewResolvesStrategyAndChunks(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"retain_extraction_mode":  "chunks",
		"retain_default_strategy": "verbose_one",
		"retain_strategies": map[string]any{
			"verbose_one": map[string]any{"retain_extraction_mode": "verbose"},
			"terse_one":   map[string]any{"retain_mission": "Only terse facts."},
		},
	}
	if _, err := store.UpdateBank(ctx, bank, storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	mux := reportingMux(store)
	preview := func(body string) PromptPreviewResponse {
		rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/prompts/preview", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview %s: %d, body = %s", body, rec.Code, rec.Body.String())
		}
		var payload PromptPreviewResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}

	// No strategy named: the bank's default applies, overriding the mode to
	// verbose, so the preview renders a prompt and echoes the default's name.
	resolved := preview(`{"operation":"retain"}`)
	if resolved.Strategy == nil || *resolved.Strategy != "verbose_one" {
		t.Fatalf("strategy = %v, want the bank's default", resolved.Strategy)
	}
	if len(resolved.Strategies) != 2 || resolved.Strategies[0] != "terse_one" || resolved.Strategies[1] != "verbose_one" {
		t.Fatalf("strategies = %v, want the sorted configured names", resolved.Strategies)
	}
	if resolved.SkippedReason != nil {
		t.Fatalf("a verbose strategy must render a prompt, got skipped: %v", *resolved.SkippedReason)
	}

	// A named strategy that leaves the mode on chunks skips the render, and the
	// effective name is echoed.
	skipped := preview(`{"operation":"retain","strategy":"terse_one"}`)
	if skipped.Strategy == nil || *skipped.Strategy != "terse_one" {
		t.Fatalf("strategy = %v, want the requested name", skipped.Strategy)
	}
	if skipped.SkippedReason == nil || !strings.Contains(*skipped.SkippedReason, "Chunks mode") {
		t.Fatalf("skipped_reason = %v, want the chunks-mode explanation", skipped.SkippedReason)
	}
	if len(skipped.Messages) != 1 || skipped.Messages[0].Role != "system" || len(skipped.Messages[0].Blocks) != 1 {
		t.Fatalf("chunks preview messages = %+v, want one system block", skipped.Messages)
	}
	block := skipped.Messages[0].Blocks[0]
	if block.Field != "retain_extraction_mode" || block.Active || block.Value == nil || *block.Value != "chunks" || !block.Editable {
		t.Fatalf("chunks mode block = %+v", block)
	}
	if len(skipped.RunSettings) != 2 {
		t.Fatalf("run_settings = %+v, want the two chunk sizes", skipped.RunSettings)
	}

	// Consolidation has no strategies: neither the response strategy nor the
	// list carries one, even when the caller names one.
	consolidated := preview(`{"operation":"consolidation","strategy":"verbose_one"}`)
	if consolidated.Strategy != nil {
		t.Fatalf("consolidation strategy = %v, want null", consolidated.Strategy)
	}
	if len(consolidated.Strategies) != 0 {
		t.Fatalf("consolidation strategies = %v, want none", consolidated.Strategies)
	}
	if consolidated.ResponseSchema != nil {
		t.Fatalf("consolidation response_schema = %v, want null", consolidated.ResponseSchema)
	}
}

// With a label vocabulary configured, the entity-labels section is split around
// the one line entities_allow_free_form decides, so the flag is reported as its
// own block rather than hidden inside the labels text.
func TestPromptPreviewSplitsEntityLabelsSection(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"retain_extraction_mode":   "concise",
		"entities_allow_free_form": false,
		"entity_labels": []any{
			map[string]any{"key": "pedagogy", "type": "value", "values": []any{map[string]any{"value": "scaffolding"}, map[string]any{"value": "inquiry"}}},
			map[string]any{"key": "interest", "type": "text"},
		},
	}
	if _, err := store.UpdateBank(ctx, bank, storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	mux := reportingMux(store)
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/prompts/preview", `{"operation":"retain"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload PromptPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var labels, freeForm int
	for _, message := range payload.Messages {
		for _, block := range message.Blocks {
			switch block.Field {
			case "entity_labels":
				labels++
				if block.Kind != "complex" || !block.Editable {
					t.Fatalf("entity_labels block = %+v", block)
				}
			case "entities_allow_free_form":
				freeForm++
				if block.Kind != "boolean" || block.Value == nil || *block.Value != "false" || !block.Editable {
					t.Fatalf("entities_allow_free_form block = %+v", block)
				}
				if !strings.Contains(block.Text, "labels-only mode") {
					t.Fatalf("free-form block text = %q", block.Text)
				}
			}
		}
	}
	if labels < 2 || freeForm != 1 {
		t.Fatalf("labels blocks = %d, free-form blocks = %d, want the section split into two label blocks and one flag line", labels, freeForm)
	}
}

// The retain preview reports the reference's extraction response JSON schema —
// the pydantic document of the model the deployment config selects — verbatim,
// and reports none for the operations that declare no response model.
func TestPromptPreviewServesExtractionResponseSchema(t *testing.T) {
	type schemaResponse struct {
		ResponseSchema json.RawMessage `json:"response_schema"`
	}
	// previewFor builds a server whose retain switches are the ones under test
	// and whose bank carries the given config, then returns the retain preview.
	previewFor := func(t *testing.T, config map[string]any, causal *bool, optionalDimensions bool) json.RawMessage {
		t.Helper()
		store := storage.NewMemoryStore("public")
		ctx := context.Background()
		if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
			t.Fatal(err)
		}
		if config != nil {
			if _, err := store.UpdateBank(ctx, bank, storage.BankPatch{Config: &config}); err != nil {
				t.Fatal(err)
			}
		}
		mux := http.NewServeMux()
		handlers := NewHandlers(store)
		handlers.RetainExtractCausalLinks = causal
		handlers.RetainOptionalFactDimensions = optionalDimensions
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
		rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/prompts/preview", `{"operation":"retain"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview = %d, body = %s", rec.Code, rec.Body.String())
		}
		var payload schemaResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.ResponseSchema
	}
	// expected reads the committed document and reports the title it announces.
	expected := func(t *testing.T, name string) (json.RawMessage, string) {
		t.Helper()
		data, err := extractionResponseSchemas.ReadFile("extraction_schemas/" + name + ".json")
		if err != nil {
			t.Fatalf("missing committed schema %s: %v", name, err)
		}
		var head struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			t.Fatalf("%s is not a JSON document: %v", name, err)
		}
		return json.RawMessage(data), head.Title
	}
	falseValue := false
	cases := []struct {
		name        string
		config      map[string]any
		causal      *bool
		optional    bool
		wantVariant string
	}{
		{name: "concise default", wantVariant: "retain"},
		{name: "custom falls back to the concise model", config: map[string]any{"retain_extraction_mode": "custom"}, wantVariant: "retain"},
		{name: "verbose", config: map[string]any{"retain_extraction_mode": "verbose"}, wantVariant: "retain_verbose"},
		{name: "verbatim", config: map[string]any{"retain_extraction_mode": "verbatim"}, wantVariant: "retain_verbatim"},
		{name: "causal links disabled", causal: &falseValue, wantVariant: "retain_no_causal"},
		{name: "optional fact dimensions", optional: true, wantVariant: "retain_optional_dimensions"},
		{name: "verbose without causal links", config: map[string]any{"retain_extraction_mode": "verbose"}, causal: &falseValue, wantVariant: "retain_no_causal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, title := expected(t, tc.wantVariant)
			got := previewFor(t, tc.config, tc.causal, tc.optional)
			if string(got) != string(want) {
				t.Fatalf("response_schema is not the committed %s document:\n got %s\nwant %s", tc.wantVariant, got, want)
			}
			var head struct {
				Title string `json:"title"`
			}
			if err := json.Unmarshal(got, &head); err != nil {
				t.Fatal(err)
			}
			if head.Title != title {
				t.Fatalf("response_schema title = %q, want %q", head.Title, title)
			}
		})
	}
	// The reference serves the pydantic model's document, so the shipped default
	// must announce the model's own name and reference its extracted-fact model,
	// not a set of Go field names.
	want, _ := expected(t, "retain")
	if !strings.Contains(string(want), `"title":"FactExtractionResponse"`) {
		t.Fatalf("the default document does not name the reference model: %s", want)
	}
	if !strings.Contains(string(want), `"$ref":"#/$defs/ExtractedFact"`) {
		t.Fatalf("the default document does not reference its fact model: %s", want)
	}
	if got := previewFor(t, nil, nil, false); !strings.Contains(string(got), `"$defs"`) {
		t.Fatalf("the served schema is not the pydantic document: %s", got)
	}
	// Consolidation has no response model, so its preview reports none — and so
	// does reflect, whose schema is per-request rather than config-selected.
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
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
	for _, operation := range []string{"consolidation", "reflect"} {
		rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/prompts/preview", `{"operation":"`+operation+`"}`)
		var payload schemaResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(payload.ResponseSchema)) != "null" {
			t.Fatalf("%s preview response_schema = %s, want null", operation, payload.ResponseSchema)
		}
	}
}

// A bank-wide observation clear against an unknown bank is an empty clear, not a
// not-found: the reference takes no bank-existence check on this route, so the
// delete simply matches no rows.
func TestDeleteObservationsUnknownBankIsEmptyClear(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(pattern, "{bank_id}") {
				r.SetPathValue("bank_id", "ghost")
			}
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodDelete, "/v1/default/banks/ghost/observations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown-bank clear = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp DeleteResponse
	jsonUnmarshal(t, rec.Body.Bytes(), &resp)
	if !resp.Success || resp.DeletedCount != 0 || resp.Message != "Cleared 0 observations" {
		t.Fatalf("resp = %+v", resp)
	}
}

// The memory-observations clear takes no bank-existence check either: the
// reference resolves the memory by id alone, so a delete against an unknown
// bank clears zero rows and answers 200 rather than 404ing the bank.
func TestClearMemoryObservationsUnknownBankIsEmptyClear(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(pattern, "{bank_id}") {
				r.SetPathValue("bank_id", "ghost")
			}
			if strings.Contains(pattern, "{memory_id}") {
				r.SetPathValue("memory_id", "00000000-0000-0000-0000-000000000000")
			}
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodDelete,
		"/v1/default/banks/ghost/memories/00000000-0000-0000-0000-000000000000/observations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown-bank memory clear = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp ClearMemoryObservationsResponse
	jsonUnmarshal(t, rec.Body.Bytes(), &resp)
	if resp.DeletedCount != 0 {
		t.Fatalf("deleted_count = %d, want 0", resp.DeletedCount)
	}
}

// The memory listing carries the attachments each fact was drawn from, and an
// observation shows its source facts'. A fact with none omits the key.
func TestListMemoriesCarriesAttachments(t *testing.T) {
	mux, store := newServerWithStore(t)
	ctx := context.Background()
	const sourceID = "11111111-1111-1111-1111-111111111111"
	const observationID = "22222222-2222-2222-2222-222222222222"
	if err := store.PutAttachment(ctx, storage.Attachment{
		BankID: bank, DocumentID: "doc-att", AttachmentHash: "hash-1", ShortID: "abc123",
		MediaType: "image/png", ByteSize: 12, StorageKey: "k", Kind: "image", Filename: "chart.png",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: sourceID, BankID: bank, Text: "a chart", FactType: storage.TypeExperience,
		DocumentID: "doc-att", AttachmentIDs: []string{"abc123"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: observationID, BankID: bank, Text: "obs", FactType: storage.TypeObservation,
		SourceMemoryIDs: []string{sourceID},
	}); err != nil {
		t.Fatal(err)
	}
	rec := send(mux, http.MethodGet, "/v1/default/banks/"+bank+"/memories/list?limit=100", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list MemoryListResponse
	jsonUnmarshal(t, rec.Body.Bytes(), &list)
	byID := map[string]MemoryItem{}
	for _, item := range list.Items {
		byID[item.ID] = item
	}
	source, ok := byID[sourceID]
	if !ok || len(source.Attachments) != 1 {
		t.Fatalf("source item = %+v", source)
	}
	got := source.Attachments[0]
	if got["id"] != "abc123" || got["hash"] != "hash-1" || got["kind"] != "image" ||
		got["media_type"] != "image/png" || got["byte_size"] != float64(12) ||
		got["filename"] != "chart.png" || got["url"] != "/v1/default/banks/b1/attachments/abc123" {
		t.Fatalf("attachment = %+v", got)
	}
	// The observation inherits its source fact's attachment.
	if observation, ok := byID[observationID]; !ok || len(observation.Attachments) != 1 {
		t.Fatalf("observation item = %+v", observation)
	}
	// The fixture's own fact carries no attachment, so the key is omitted.
	plain := byID[memoryID(t, mux)]
	if plain.Attachments != nil {
		t.Fatalf("plain item carries attachments: %+v", plain.Attachments)
	}
}

// A prompt preview's body is a declared model, so the reference's framework
// answers a body that is not an object with its `model_attributes_type` item,
// and an absent body — or the JSON literal `null`, which is how a model with no
// required members decodes when nothing was sent — with the `missing` item.
//
// Both shapes were wrong here: a list body produced the decoder's plain
// `dict_type` ("Input should be a valid dictionary"), and a `null` body got past
// the empty-body guard and rendered a real preview with 200 where the reference
// answers 422.
func TestPromptPreviewBodyShapesMatchTheReference(t *testing.T) {
	mux := newServer(t)
	preview := "/v1/default/banks/" + bank + "/prompts/preview"
	for _, tc := range []struct {
		name string
		body string
		want []string // every substring the body must carry
	}{
		{name: "list", body: `[]`, want: []string{"model_attributes_type", "Input should be a valid dictionary or object to extract fields from"}},
		{name: "number", body: `5`, want: []string{"model_attributes_type", "Input should be a valid dictionary or object to extract fields from"}},
		{name: "string", body: `"x"`, want: []string{"model_attributes_type", "Input should be a valid dictionary or object to extract fields from"}},
		{name: "null", body: `null`, want: []string{"missing", "Field required"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(mux, http.MethodPost, preview, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("body = %s, want it to contain %q", rec.Body.String(), want)
				}
			}
		})
	}
}
