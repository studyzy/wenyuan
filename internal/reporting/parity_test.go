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

// parityServer builds the route table over a store seeded with a world fact and
// an observation whose source is the world fact.
func parityServer(t *testing.T) *http.ServeMux {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "mem-world", BankID: bank, Text: "the deployment runs on Tuesdays",
		FactType: storage.TypeWorld, Tags: []string{"ops"}, DocumentID: "doc-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "mem-obs", BankID: bank, Text: "deployments happen weekly",
		FactType: storage.TypeObservation, SourceMemoryIDs: []string{"mem-world"},
	}); err != nil {
		t.Fatal(err)
	}
	return reportingMux(store)
}

// reportingMux binds the handlers with path values set, as the router does.
func reportingMux(store storage.Store) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
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

func decodeObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return body
}

// finding 1: the list item names the type `fact_type` and validates its filters.
func TestMemoryListFactTypeAndFilters(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodGet, base+"/memories/list", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []map[string]any `json:"items"`
		Limit int              `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(list.Items))
	}
	item := list.Items[0]
	if _, ok := item["fact_type"]; !ok {
		t.Fatalf("item must carry fact_type: %v", item)
	}
	if _, ok := item["type"]; ok {
		t.Fatalf("item must not carry type: %v", item)
	}
	if list.Limit != 100 {
		t.Fatalf("limit default = %d, want 100", list.Limit)
	}

	// An out-of-enum tags_match is a 422, not a silently ignored filter.
	if rec := send(mux, http.MethodGet, base+"/memories/list?tags_match=bad", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad tags_match = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	// An unknown state is a 400.
	if rec := send(mux, http.MethodGet, base+"/memories/list?state=consolidated", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad state = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodGet, base+"/memories/list?consolidation_state=weird", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad consolidation_state = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// finding 1: state/consolidation_state filter against storage rather than being
// ignored, and the invalidated unit reports its invalidation.
func TestMemoryStateFilter(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodPatch, base+"/memories/mem-world", `{"state":"invalidated","reason":"superseded"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("invalidate = %d: %s", rec.Code, rec.Body.String())
	}
	detail := decodeObject(t, rec)
	if detail["state"] != "invalidated" || detail["invalidation_reason"] != "superseded" {
		t.Fatalf("invalidation not recorded: %v", detail)
	}

	count := func(path string) int {
		body := decodeObject(t, send(mux, http.MethodGet, path, ""))
		items, _ := body["items"].([]any)
		return len(items)
	}
	if got := count(base + "/memories/list"); got != 1 {
		t.Fatalf("default list = %d, want 1 (live only)", got)
	}
	if got := count(base + "/memories/list?state=invalidated"); got != 1 {
		t.Fatalf("invalidated list = %d, want 1", got)
	}
	if got := count(base + "/memories/list?state=valid"); got != 1 {
		t.Fatalf("valid list = %d, want 1", got)
	}
}

// finding 2: the detail dict carries the reference's keys and shapes.
func TestMemoryDetailShape(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodGet, base+"/memories/mem-world", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", rec.Code, rec.Body.String())
	}
	detail := decodeObject(t, rec)
	for _, key := range []string{"id", "text", "context", "date", "type", "mentioned_at",
		"occurred_start", "occurred_end", "entities", "document_id", "chunk_id", "tags",
		"metadata", "observation_scopes", "state", "invalidation_reason", "invalidated_at", "edited_at"} {
		if _, ok := detail[key]; !ok {
			t.Fatalf("detail missing %q: %v", key, detail)
		}
	}
	if _, ok := detail["entities"].([]any); !ok {
		t.Fatalf("entities must be a list, got %T", detail["entities"])
	}
	if _, ok := detail["bank_id"]; ok {
		t.Fatalf("detail must not carry bank_id: %v", detail)
	}

	// An observation also carries history and its resolved sources.
	obs := decodeObject(t, send(mux, http.MethodGet, base+"/memories/mem-obs", ""))
	if _, ok := obs["history"].([]any); !ok {
		t.Fatalf("observation detail must carry history list: %v", obs)
	}
	ids, ok := obs["source_memory_ids"].([]any)
	if !ok || len(ids) != 1 {
		t.Fatalf("source_memory_ids = %v", obs["source_memory_ids"])
	}
	sources, ok := obs["source_memories"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("source_memories = %v", obs["source_memories"])
	}
}

// finding 3: PATCH accepts the reference's body and returns the detail dict.
func TestMemoryUpdateBody(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank

	rec := send(mux, http.MethodPatch, base+"/memories/mem-world", `{"text":"updated text","reason":"fix"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	detail := decodeObject(t, rec)
	if detail["text"] != "updated text" {
		t.Fatalf("text = %v", detail["text"])
	}
	if _, ok := detail["entities"]; !ok {
		t.Fatalf("update response must be the detail dict: %v", detail)
	}
	// Empty body is a 422.
	if rec := send(mux, http.MethodPatch, base+"/memories/mem-world", `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty update = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	// A state outside the enum is a 422.
	if rec := send(mux, http.MethodPatch, base+"/memories/mem-world", `{"state":"nope"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad state = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// finding 4: the history route returns a bare array with the reference's keys.
func TestMemoryHistoryBareArray(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	// Produce a history entry.
	if rec := send(mux, http.MethodPatch, base+"/memories/mem-obs", `{"text":"changed"}`); rec.Code != http.StatusOK {
		t.Fatalf("update = %d: %s", rec.Code, rec.Body.String())
	}
	rec := send(mux, http.MethodGet, base+"/memories/mem-obs/history", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d: %s", rec.Code, rec.Body.String())
	}
	var entries []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("history must be a bare array: %v (%s)", err, rec.Body.String())
	}
	if len(entries) == 0 {
		t.Fatalf("history had no entries: %s", rec.Body.String())
	}
	for _, key := range []string{"previous_text", "previous_tags", "previous_occurred_start",
		"previous_occurred_end", "previous_mentioned_at", "changed_at", "new_source_memory_ids", "source_facts"} {
		if _, ok := entries[0][key]; !ok {
			t.Fatalf("entry missing %q: %v", key, entries[0])
		}
	}
}

// finding 5: consolidate answers 200.
func TestConsolidateReturns200(t *testing.T) {
	mux := parityServer(t)
	rec := send(mux, http.MethodPost, "/v1/default/banks/"+bank+"/consolidate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("consolidate = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// finding 6: the strategy preview uses the reference's shapes.
func TestConsolidationPreviewShape(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	body := `{"strategies":[{"scopes":[{"tags":["ops"]},{"tags":["team:*"],"tags_match":"exact"}],"max_observations_per_scope":5}],"sample_limit":3}`
	rec := send(mux, http.MethodPost, base+"/consolidation-strategies/preview", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}
	preview := decodeObject(t, rec)
	for _, key := range []string{"strategies", "default", "scopes_scanned", "complete"} {
		if _, ok := preview[key]; !ok {
			t.Fatalf("preview missing %q: %v", key, preview)
		}
	}
	if _, ok := preview["bank_id"]; ok {
		t.Fatalf("preview must not carry bank_id: %v", preview)
	}
	strategies, _ := preview["strategies"].([]any)
	if len(strategies) != 1 {
		t.Fatalf("strategies = %v", preview["strategies"])
	}
	strategy, _ := strategies[0].(map[string]any)
	for _, key := range []string{"active", "claimed_count", "rules"} {
		if _, ok := strategy[key]; !ok {
			t.Fatalf("strategy missing %q: %v", key, strategy)
		}
	}
	if _, ok := strategy["name"]; ok {
		t.Fatalf("strategy must not carry name: %v", strategy)
	}
	rules, _ := strategy["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("rules = %v", strategy["rules"])
	}
	rule, _ := rules[0].(map[string]any)
	for _, key := range []string{"match_count", "taken_count", "observation_count", "samples"} {
		if _, ok := rule[key]; !ok {
			t.Fatalf("rule missing %q: %v", key, rule)
		}
	}
	def, _ := preview["default"].(map[string]any)
	for _, key := range []string{"match_count", "observation_count", "samples"} {
		if _, ok := def[key]; !ok {
			t.Fatalf("default missing %q: %v", key, def)
		}
	}
}

// finding 7: the prompt preview reports messages as blocks and run_settings as a
// list.
func TestPromptPreviewShape(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	rec := send(mux, http.MethodPost, base+"/prompts/preview", `{"operation":"retain"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}
	preview := decodeObject(t, rec)
	messages, _ := preview["messages"].([]any)
	if len(messages) == 0 {
		t.Fatalf("no messages: %v", preview)
	}
	message, _ := messages[0].(map[string]any)
	if _, ok := message["content"]; ok {
		t.Fatalf("message must not carry content: %v", message)
	}
	blocks, _ := message["blocks"].([]any)
	if len(blocks) == 0 {
		t.Fatalf("message carried no blocks: %v", message)
	}
	block, _ := blocks[0].(map[string]any)
	for _, key := range []string{"text", "source", "field", "section", "heading", "active", "value", "kind", "choices", "editable"} {
		if _, ok := block[key]; !ok {
			t.Fatalf("block missing %q: %v", key, block)
		}
	}
	settings, ok := preview["run_settings"].([]any)
	if !ok {
		t.Fatalf("run_settings must be a list: %v", preview["run_settings"])
	}
	_ = settings
}

// finding 8: dry-run extraction uses the reference's shapes and accepts a block
// list as content.
func TestDryRunExtractShape(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	body := `{"content":[{"type":"text","text":"Alice works at Google."}],"retain_chunk_size":1000,"llm_output_language":"en"}`
	rec := send(mux, http.MethodPost, base+"/memories/dry-run-extract", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry-run = %d: %s", rec.Code, rec.Body.String())
	}
	result := decodeObject(t, rec)
	for _, key := range []string{"facts", "chunks", "usage"} {
		if _, ok := result[key]; !ok {
			t.Fatalf("result missing %q: %v", key, result)
		}
	}
	chunks, _ := result["chunks"].([]any)
	if len(chunks) == 0 {
		t.Fatalf("chunks = %v", result["chunks"])
	}
	chunk, _ := chunks[0].(map[string]any)
	for _, key := range []string{"text", "fact_count"} {
		if _, ok := chunk[key]; !ok {
			t.Fatalf("chunk missing %q: %v", key, chunk)
		}
	}
	usage, _ := result["usage"].(map[string]any)
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens", "cached_tokens", "thoughts_tokens"} {
		if _, ok := usage[key]; !ok {
			t.Fatalf("usage missing %q: %v", key, usage)
		}
	}
	// Empty content is rejected.
	if rec := send(mux, http.MethodPost, base+"/memories/dry-run-extract", `{"content":""}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty content = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// finding 10: the timeseries defaults its period/time_field and omits `count`.
func TestTimeseriesShape(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	rec := send(mux, http.MethodGet, base+"/stats/memories-timeseries", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("timeseries = %d: %s", rec.Code, rec.Body.String())
	}
	series := decodeObject(t, rec)
	if series["period"] != "7d" {
		t.Fatalf("period = %v, want 7d", series["period"])
	}
	if series["time_field"] != "created_at" {
		t.Fatalf("time_field = %v, want created_at", series["time_field"])
	}
	if series["trunc"] != "day" {
		t.Fatalf("trunc = %v, want day", series["trunc"])
	}
	buckets, _ := series["buckets"].([]any)
	if len(buckets) == 0 {
		t.Fatalf("buckets = %v", series["buckets"])
	}
	bucket, _ := buckets[0].(map[string]any)
	if _, ok := bucket["count"]; ok {
		t.Fatalf("bucket must not carry count: %v", bucket)
	}
	for _, key := range []string{"time", "world", "experience", "observation"} {
		if _, ok := bucket[key]; !ok {
			t.Fatalf("bucket missing %q: %v", key, bucket)
		}
	}
}

// finding 11: LLM token sums use the reference's field names.
func TestLLMStatsTokenKeys(t *testing.T) {
	mux := parityServer(t)
	base := "/v1/default/banks/" + bank
	rec := send(mux, http.MethodGet, base+"/llm-requests/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats = %d: %s", rec.Code, rec.Body.String())
	}
	// The route answers even with no rows; assert the model names by encoding one.
	encoded, err := json.Marshal(LLMRequestStatsBucket{Tokens: LLMTokenSums{Input: 1, Output: 2, Cached: 3, Thoughts: 4, Total: 5}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"input":`, `"output":`, `"cached":`, `"thoughts":`, `"total":`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("token sums missing %s: %s", key, encoded)
		}
	}
	if strings.Contains(string(encoded), `"input_tokens"`) {
		t.Fatalf("token sums must not use input_tokens: %s", encoded)
	}
}
