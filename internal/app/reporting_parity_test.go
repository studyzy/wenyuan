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
	if _, err := app.Store.CreateBank(ctx, storage.Bank{ID: "rp-bank", Name: "rp-bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "mem-1", BankID: "rp-bank", Text: "the deployment runs on Tuesdays",
		Type: storage.TypeWorld, Tags: []string{"ops"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "obs-1", BankID: "rp-bank", Text: "deployments happen weekly",
		Type: storage.TypeObservation,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.PutOperation(ctx, storage.Operation{
		ID: "op-1", BankID: "rp-bank", OperationType: storage.OperationTypeTransferExport,
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
		{"consolidation preview", http.MethodPost, base + "/consolidation-strategies/preview", http.StatusOK},
		{"consolidation recover", http.MethodPost, base + "/consolidation/recover", http.StatusOK},
		{"consolidate", http.MethodPost, base + "/consolidate", http.StatusAccepted},
		{"memories list", http.MethodGet, base + "/memories/list", http.StatusOK},
		{"memory get", http.MethodGet, base + "/memories/mem-1", http.StatusOK},
		{"memory history", http.MethodGet, base + "/memories/mem-1/history", http.StatusOK},
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

// test-memory-update-records-history: an update changes the memory and leaves a
// history entry behind.
func TestReportingMemoryUpdateAndHistory(t *testing.T) {
	app := reportingApp(t)
	base := "/v1/default/banks/rp-bank/memories/mem-1"

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

	rec = send(app, http.MethodGet, base+"/history", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("history = %d", rec.Code)
	}
	var history struct {
		History []map[string]any `json:"history"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.History) != 1 {
		t.Fatalf("history has %d entries after one update, want 1", len(history.History))
	}
}

// test-consolidation-is-deduplicated: a second consolidation while one is
// outstanding returns the existing operation rather than queueing duplicate work.
func TestReportingConsolidationDeduplicated(t *testing.T) {
	app := reportingApp(t)
	path := "/v1/default/banks/rp-bank/consolidate"

	first := send(app, http.MethodPost, path, "")
	if first.Code != http.StatusAccepted {
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
	folderRec := send(app, http.MethodPost, base+"/folders", `{"title":"ops"}`)
	if folderRec.Code != http.StatusCreated {
		t.Fatalf("create folder = %d (body %s)", folderRec.Code, folderRec.Body.String())
	}
	var folder struct {
		Page struct {
			ID string `json:"id"`
		} `json:"page"`
	}
	if err := json.Unmarshal(folderRec.Body.Bytes(), &folder); err != nil {
		t.Fatal(err)
	}

	pageRec := send(app, http.MethodPost, base+"/pages",
		`{"title":"deploy runbook","text":"deployments run on Tuesdays","parent_id":"`+folder.Page.ID+`"}`)
	if pageRec.Code != http.StatusCreated {
		t.Fatalf("create page = %d (body %s)", pageRec.Code, pageRec.Body.String())
	}
	var page struct {
		Page struct {
			ID string `json:"id"`
		} `json:"page"`
	}
	if err := json.Unmarshal(pageRec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}

	// The tree nests the page under its folder.
	treeRec := send(app, http.MethodGet, base+"/tree", "")
	var tree struct {
		Nodes []map[string]any `json:"nodes"`
	}
	if err := json.Unmarshal(treeRec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 1 {
		t.Fatalf("tree has %d roots, want 1: %v", len(tree.Nodes), tree.Nodes)
	}
	children, _ := tree.Nodes[0]["children"].([]any)
	if len(children) != 1 {
		t.Fatalf("the folder must hold one child, got %v", tree.Nodes[0]["children"])
	}

	// Search finds the page by its body text.
	searchRec := send(app, http.MethodGet, base+"/search?q=Tuesdays", "")
	var search struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal(searchRec.Body.Bytes(), &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Hits) != 1 {
		t.Fatalf("search returned %d hits, want 1", len(search.Hits))
	}

	// Deleting the folder removes the page with it, so the tree cannot hold an
	// unreachable node.
	delRec := send(app, http.MethodDelete, base+"/nodes/"+folder.Page.ID, "")
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("delete folder = %d (body %s)", delRec.Code, delRec.Body.String())
	}
	pageGet := send(app, http.MethodGet, base+"/pages/"+page.Page.ID, "")
	if pageGet.Code != http.StatusNotFound {
		t.Fatalf("a page inside a deleted folder must be gone, got %d", pageGet.Code)
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
		{"unknown memory", http.MethodGet, base + "/memories/absent"},
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
