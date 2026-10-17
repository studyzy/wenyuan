package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// seedReporting creates a bank with a memory, an observation and an operation,
// so every reporting route has something to report.
func seedReporting(t *testing.T, app *App) {
	t.Helper()
	ctx := context.Background()
	if _, err := app.Store.CreateBank(ctx, storage.Bank{BankID: "rp-bank", Name: "rp-bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "11111111-1111-1111-1111-111111111111", BankID: "rp-bank", Text: "the deployment runs on Tuesdays",
		FactType: storage.TypeWorld, Tags: []string{"ops"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "33333333-3333-3333-3333-333333333333", BankID: "rp-bank", Text: "deployments happen weekly",
		FactType: storage.TypeObservation,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.PutOperation(ctx, storage.Operation{
		OperationID: "op-1", BankID: "rp-bank", OperationType: storage.OperationTypeTransferExport,
		Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
}

// reportingApp composes an in-memory application with the reporting fixture.
func reportingApp(t *testing.T) *App {
	t.Helper()
	app := newParityApp(t)
	seedReporting(t, app)
	return app
}

// test-reporting-routes-resolve: every observability, memory sub-route and
// consolidation route answers through the served handler.
func TestReportingRoutesResolve(t *testing.T) {
	app := reportingApp(t)
	base := "/v1/default/banks/rp-bank"

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"stats", http.MethodGet, base + "/stats", http.StatusOK},
		{"timeseries", http.MethodGet, base + "/stats/memories-timeseries", http.StatusOK},
		{"llm health", http.MethodPost, base + "/health/llm", http.StatusOK},
		{"audit logs", http.MethodGet, base + "/audit-logs", http.StatusOK},
		{"audit stats", http.MethodGet, base + "/audit-logs/stats", http.StatusOK},
		{"llm requests", http.MethodGet, base + "/llm-requests", http.StatusOK},
		{"llm request stats", http.MethodGet, base + "/llm-requests/stats", http.StatusOK},
		{"observations delete", http.MethodDelete, base + "/observations", http.StatusOK},
		{"observation scopes", http.MethodGet, base + "/observations/scopes", http.StatusOK},
		{"consolidation preview", http.MethodPost, base + "/consolidation-strategies/preview", http.StatusUnprocessableEntity},
		{"consolidation recover", http.MethodPost, base + "/consolidation/recover", http.StatusOK},
		{"consolidate", http.MethodPost, base + "/consolidate", http.StatusOK},
		{"memories list", http.MethodGet, base + "/memories/list", http.StatusOK},
		{"memory get", http.MethodGet, base + "/memories/11111111-1111-1111-1111-111111111111", http.StatusOK},
		{"memory history", http.MethodGet, base + "/memories/11111111-1111-1111-1111-111111111111/history", http.StatusOK},
		{"prompt preview", http.MethodPost, base + "/prompts/preview", http.StatusOK},
		{"knowledge tree", http.MethodGet, base + "/knowledge-base/tree", http.StatusOK},
		{"knowledge export", http.MethodGet, base + "/knowledge-base/export", http.StatusOK},
		{"knowledge search", http.MethodGet, base + "/knowledge-base/search?q=deploy", http.StatusOK},
		{"bank template schema", http.MethodGet, "/v1/bank-template-schema", http.StatusOK},
		{"version v1", http.MethodGet, "/v1/version", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := ""
			if tc.method == http.MethodPost {
				body = `{"operation":"retain"}`
			}
			rec := send(app, tc.method, tc.path, body)
			if rec.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d (body %s)", tc.method, tc.path, rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// test-stats-reports-the-bank: the summary counts what the bank actually holds.
func TestReportingStatsCounts(t *testing.T) {
	app := reportingApp(t)
	rec := send(app, http.MethodGet, "/v1/default/banks/rp-bank/stats", "")
	var stats struct {
		BankID            string         `json:"bank_id"`
		TotalNodes        int            `json:"total_nodes"`
		NodesByFactType   map[string]int `json:"nodes_by_fact_type"`
		PendingOperations int            `json:"pending_operations"`
		TotalObservations int            `json:"total_observations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.BankID != "rp-bank" {
		t.Fatalf("bank id = %q", stats.BankID)
	}
	if stats.TotalNodes != 2 {
		t.Fatalf("total nodes = %d, want 2", stats.TotalNodes)
	}
	if stats.NodesByFactType["world"] != 1 || stats.NodesByFactType["observation"] != 1 {
		t.Fatalf("nodes by fact type = %v", stats.NodesByFactType)
	}
	if stats.PendingOperations != 1 {
		t.Fatalf("pending operations = %d, want 1", stats.PendingOperations)
	}
	if stats.TotalObservations != 1 {
		t.Fatalf("total observations = %d, want 1", stats.TotalObservations)
	}
}

// test-memory-update-records-history: an update changes the memory; only an
// observation carries a history, a source fact reports an empty array.
func TestReportingMemoryUpdateAndHistory(t *testing.T) {
	app := reportingApp(t)
	base := "/v1/default/banks/rp-bank/memories/11111111-1111-1111-1111-111111111111"

	rec := send(app, http.MethodPatch, base, `{"text":"the deployment now runs on Wednesdays","tags":["ops","changed"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d (body %s)", rec.Code, rec.Body.String())
	}
	var updated map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated["text"] != "the deployment now runs on Wednesdays" {
		t.Fatalf("update did not apply: %v", updated)
	}

	// A source fact has no history.
	rec = send(app, http.MethodGet, base+"/history", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d", rec.Code)
	}
	var worldHistory []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &worldHistory); err != nil {
		t.Fatalf("history must be a bare array: %v (%s)", err, rec.Body.String())
	}
	if len(worldHistory) != 0 {
		t.Fatalf("a source fact must report no history, got %d", len(worldHistory))
	}

	// A derived observation is not curatable: the reference refuses the edit and
	// the observation regenerates from its sources instead.
	obs := "/v1/default/banks/rp-bank/memories/33333333-3333-3333-3333-333333333333"
	if rec := send(app, http.MethodPatch, obs, `{"text":"deployments happen every week"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("observation update = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	rec = send(app, http.MethodGet, obs+"/history", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("observation history = %d", rec.Code)
	}
	var history []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatalf("history must be a bare array: %v (%s)", err, rec.Body.String())
	}
	if len(history) != 0 {
		t.Fatalf("history has %d entries after a refused edit, want 0", len(history))
	}
}

// test-consolidation-is-deduplicated: a second consolidation while one is
// outstanding returns the existing operation rather than queueing duplicate work.
func TestReportingConsolidationDeduplicated(t *testing.T) {
	app := reportingApp(t)
	path := "/v1/default/banks/rp-bank/consolidate"

	first := send(app, http.MethodPost, path, "")
	if first.Code != http.StatusOK {
		t.Fatalf("first consolidate = %d (body %s)", first.Code, first.Body.String())
	}
	var firstBody struct {
		OperationID  string `json:"operation_id"`
		Deduplicated bool   `json:"deduplicated"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstBody); err != nil {
		t.Fatal(err)
	}
	if firstBody.Deduplicated {
		t.Fatal("the first consolidation must not report itself as deduplicated")
	}

	second := send(app, http.MethodPost, path, "")
	var secondBody struct {
		OperationID  string `json:"operation_id"`
		Deduplicated bool   `json:"deduplicated"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil {
		t.Fatal(err)
	}
	if !secondBody.Deduplicated {
		t.Fatal("a second consolidation must be deduplicated")
	}
	if secondBody.OperationID != firstBody.OperationID {
		t.Fatalf("deduplicated request returned %q, want %q", secondBody.OperationID, firstBody.OperationID)
	}
}

// test-knowledge-tree-nests-and-deletes-descendants: the tree reports the
// hierarchy, and deleting a folder removes what it held.
func TestReportingKnowledgeTreeLifecycle(t *testing.T) {
	app := reportingApp(t)
	base := "/v1/default/banks/rp-bank/knowledge-base"

	// A folder, then a page inside it.
	folderRec := send(app, http.MethodPost, base+"/folders", `{"name":"ops"}`)
	if folderRec.Code != http.StatusCreated {
		t.Fatalf("create folder = %d (body %s)", folderRec.Code, folderRec.Body.String())
	}
	var folder struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(folderRec.Body.Bytes(), &folder); err != nil {
		t.Fatal(err)
	}

	pageRec := send(app, http.MethodPost, base+"/pages",
		`{"name":"deploy runbook","source_query":"when do deployments run","parent_id":"`+folder.ID+`"}`)
	if pageRec.Code != http.StatusCreated {
		t.Fatalf("create page = %d (body %s)", pageRec.Code, pageRec.Body.String())
	}
	var page struct {
		PageID string `json:"page_id"`
	}
	if err := json.Unmarshal(pageRec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.PageID == "" {
		t.Fatalf("create page must return a page_id")
	}

	// The tree nests the page under its folder.
	treeRec := send(app, http.MethodGet, base+"/tree", "")
	var tree struct {
		Roots []map[string]any `json:"roots"`
	}
	if err := json.Unmarshal(treeRec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Roots) != 1 {
		t.Fatalf("tree has %d roots, want 1: %v", len(tree.Roots), tree.Roots)
	}
	children, _ := tree.Roots[0]["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("the folder must hold one child, got %v", tree.Roots[0]["children"])
	}

	// Search finds the page: without a configured model a page has no body, so
	// the text arm matches on the page name.
	searchRec := send(app, http.MethodGet, base+"/search?q=runbook", "")
	var search struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(searchRec.Body.Bytes(), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Results) != 1 {
		t.Fatalf("search returned %d results, want 1", len(search.Results))
	}

	// Deleting the folder removes the page with it, so the tree cannot hold an
	// unreachable node.
	delRec := send(app, http.MethodDelete, base+"/nodes/"+folder.ID, "")
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete folder = %d (body %s)", delRec.Code, delRec.Body.String())
	}
	pageGet := send(app, http.MethodGet, base+"/pages/"+page.PageID, "")
	if pageGet.Code != http.StatusNotFound {
		t.Fatalf("a page inside a deleted folder must be gone, got %d", pageGet.Code)
	}
}

// test-reporting-memory-nulls-kept: the memory list and detail models keep
// their declared null fields, as the reference's route class never applies
// exclude_none.
func TestReportingMemoryKeepsDeclaredNulls(t *testing.T) {
	app := reportingApp(t)
	base := "/v1/default/banks/rp-bank"

	listRec := send(app, http.MethodGet, base+"/memories/list", "")
	if listRec.Code != http.StatusOK {
		t.Fatalf("list = %d (body %s)", listRec.Code, listRec.Body.String())
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		t.Fatalf("no items: %s", listRec.Body.String())
	}
	for _, key := range []string{"fact_type", "invalidation_reason", "invalidated_at", "updated_at"} {
		if _, ok := list.Items[0][key]; !ok {
			t.Fatalf("list item dropped declared null field %q: %v", key, list.Items[0])
		}
	}

	detailRec := send(app, http.MethodGet, base+"/memories/11111111-1111-1111-1111-111111111111", "")
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail = %d (body %s)", detailRec.Code, detailRec.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"invalidation_reason", "invalidated_at", "chunk_id"} {
		if _, ok := detail[key]; !ok {
			t.Fatalf("detail dropped declared null field %q: %v", key, detail)
		}
	}
	if _, ok := detail["bank_id"]; ok {
		t.Fatalf("detail must not carry bank_id: %v", detail)
	}
}

// test-reporting-not-found: the families report not-found for unknown
// identifiers rather than an empty success.
func TestReportingNotFound(t *testing.T) {
	app := reportingApp(t)
	base := "/v1/default/banks/rp-bank"
	unknownBank := "/v1/default/banks/ghost"

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"unknown memory", http.MethodGet, base + "/memories/00000000-0000-0000-0000-000000000000"},
		{"unknown knowledge page", http.MethodGet, base + "/knowledge-base/pages/absent"},
		{"unknown knowledge node", http.MethodDelete, base + "/knowledge-base/nodes/absent"},
		{"unknown bank stats", http.MethodGet, unknownBank + "/stats"},
		{"unknown bank memories", http.MethodGet, unknownBank + "/memories/list"},
		{"unknown bank knowledge", http.MethodGet, unknownBank + "/knowledge-base/tree"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(app, tc.method, tc.path, "")
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s %s = %d, want %d (body %s)", tc.method, tc.path, rec.Code, http.StatusNotFound, rec.Body.String())
			}
		})
	}
}
