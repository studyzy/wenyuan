package app

// Audit trail integration oracles.
//
// These cases assert the served application records its mutating and core
// operations on the audit trail, honours the deployment switch and the
// per-bank override, and leaves a record when an operation fails.

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/storage"
)

// auditApp composes the served application over an in-memory store with the
// bank configuration API available and the audit switch set as given.
func auditApp(t *testing.T, enabled bool) *App {
	t.Helper()
	cfg := testConfig()
	cfg.EnableBankConfigAPI = true
	cfg.AuditLogEnabled = enabled
	// Port 0 lets the listen probe take a free port rather than colliding with
	// a server already bound to the default.
	cfg.Port = 0
	a, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// auditRows reads one bank's audit trail through the served route, applying the
// given query string.
func auditRows(t *testing.T, a *App, bank, query string) []map[string]any {
	t.Helper()
	rec := exchange(a, http.MethodGet, "/v1/default/banks/"+bank+"/audit-logs?"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit logs: status %d, body = %s", rec.Code, rec.Body.String())
	}
	raw, _ := decodeJSON(t, rec)["items"].([]any)
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if row, ok := item.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// actionsByTransport indexes a page of rows by action name.
func actionsByTransport(rows []map[string]any) map[string]string {
	indexed := map[string]string{}
	for _, row := range rows {
		action, _ := row["action"].(string)
		transport, _ := row["transport"].(string)
		indexed[action] = transport
	}
	return indexed
}

// Every HTTP route's action is part of the deployment-wide enumeration, so the
// roster and the route table cannot drift apart.
func TestHTTPAuditActionsAreEnumerated(t *testing.T) {
	known := map[string]bool{}
	for _, action := range audit.Actions {
		known[action] = true
	}
	for key, route := range httpAuditRoutes {
		if !known[route.action] {
			t.Errorf("route %q maps to unlisted action %q", key, route.action)
		}
	}
}

// A retain, a recall, a reflect and a bank create/update each record one row
// with the reference's action name and the http transport.
func TestAuditRecordsCoreOperations(t *testing.T) {
	a := auditApp(t, true)
	const bankPath = "/v1/default/banks/audit-core"

	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Audit"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPost, bankPath+"/memories", `{"items":[{"content":"the deployment window is Thursday"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("retain: status %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPost, bankPath+"/memories/recall", `{"query":"deployment window"}`); rec.Code != http.StatusOK {
		t.Fatalf("recall: status %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPost, bankPath+"/reflect", `{"query":"when is the deployment window?"}`); rec.Code != http.StatusOK {
		t.Fatalf("reflect: status %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPatch, bankPath, `{"name":"Audit Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("update bank: status %d, body = %s", rec.Code, rec.Body.String())
	}

	rows := auditRows(t, a, "audit-core", "limit=50")
	indexed := actionsByTransport(rows)
	for _, action := range []string{"create_bank", "retain", "recall", "reflect", "update_bank"} {
		if indexed[action] != "http" {
			t.Errorf("action %q: transport = %q (want http); rows = %v", action, indexed[action], rows)
		}
	}

	// The record carries the request and response payloads the reference
	// records.
	for _, row := range rows {
		if row["action"] == "recall" {
			if request, _ := row["request"].(map[string]any); request["query"] != "deployment window" {
				t.Errorf("recall request not recorded: %v", row["request"])
			}
			if _, ok := row["response"].(map[string]any); !ok {
				t.Errorf("recall response not recorded: %v", row["response"])
			}
		}
	}
}

// The action and transport filters narrow the served listing.
func TestAuditListingFilters(t *testing.T) {
	a := auditApp(t, true)
	const bankPath = "/v1/default/banks/audit-filter"
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Filter"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d", rec.Code)
	}
	if rec := exchange(a, http.MethodPost, bankPath+"/memories", `{"items":[{"content":"hello"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("retain: status %d", rec.Code)
	}

	onlyRetain := auditRows(t, a, "audit-filter", "action=retain&limit=50")
	if len(onlyRetain) == 0 {
		t.Fatalf("action filter returned no rows")
	}
	for _, row := range onlyRetain {
		if row["action"] != "retain" {
			t.Errorf("action filter leaked %v", row["action"])
		}
	}
	onlyMCP := auditRows(t, a, "audit-filter", "transport=mcp&limit=50")
	if len(onlyMCP) != 0 {
		t.Errorf("transport filter returned %d rows, want 0", len(onlyMCP))
	}
}

// With the deployment switch off, no operation records a row.
func TestAuditDisabledProducesNoRows(t *testing.T) {
	a := auditApp(t, false)
	if rec := exchange(a, http.MethodPut, "/v1/default/banks/audit-off", `{"name":"Off"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d", rec.Code)
	}
	if rec := exchange(a, http.MethodPost, "/v1/default/banks/audit-off/memories", `{"items":[{"content":"hello"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("retain: status %d", rec.Code)
	}
	if rows := auditRows(t, a, "audit-off", "limit=50"); len(rows) != 0 {
		t.Fatalf("disabled switch recorded %d rows: %v", len(rows), rows)
	}
}

// A per-bank override turns auditing on for one bank while the deployment
// default stays off, so only that bank records rows.
func TestAuditBankOverrideFlipsOneBank(t *testing.T) {
	a := auditApp(t, false)
	for _, bank := range []string{"override-on", "override-off"} {
		if rec := exchange(a, http.MethodPut, "/v1/default/banks/"+bank, `{"name":"B"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("create %s: status %d", bank, rec.Code)
		}
	}
	// Turn auditing on for one bank only, through the bank configuration API.
	if rec := exchange(a, http.MethodPatch, "/v1/default/banks/override-on/config",
		`{"updates":{"audit_log_enabled":true}}`); rec.Code != http.StatusOK {
		t.Fatalf("enable override: status %d, body = %s", rec.Code, rec.Body.String())
	}

	for _, bank := range []string{"override-on", "override-off"} {
		if rec := exchange(a, http.MethodPost, "/v1/default/banks/"+bank+"/memories", `{"items":[{"content":"hello"}]}`); rec.Code != http.StatusOK {
			t.Fatalf("retain %s: status %d", bank, rec.Code)
		}
	}

	on := auditRows(t, a, "override-on", "limit=50")
	if len(on) == 0 {
		t.Fatalf("overridden bank recorded no rows")
	}
	for _, row := range on {
		if row["action"] != "retain" {
			t.Errorf("overridden bank recorded %v", row["action"])
		}
	}
	if off := auditRows(t, a, "override-off", "limit=50"); len(off) != 0 {
		t.Fatalf("default-off bank recorded %d rows: %v", len(off), off)
	}
}

// A failed operation still records a row, carrying the failure text.
func TestAuditRecordsFailingOperation(t *testing.T) {
	a := auditApp(t, true)
	const bankPath = "/v1/default/banks/audit-fail"
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Fail"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d", rec.Code)
	}
	// Cancelling an operation that does not exist fails with 404.
	fail := exchange(a, http.MethodDelete, bankPath+"/operations/00000000-0000-4000-8000-000000000000", "")
	if fail.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown operation: status %d, body = %s", fail.Code, fail.Body.String())
	}

	rows := auditRows(t, a, "audit-fail", "action=cancel_operation&limit=50")
	if len(rows) != 1 {
		t.Fatalf("expected one failure row, got %d: %v", len(rows), rows)
	}
	if rows[0]["transport"] != "http" {
		t.Errorf("failure transport = %v", rows[0]["transport"])
	}
	metadata, _ := rows[0]["metadata"].(map[string]any)
	if metadata == nil {
		t.Fatalf("failure row carries no metadata: %v", rows[0])
	}
	if text, _ := metadata["error"].(string); text == "" {
		t.Fatalf("failure text not recorded: %v", metadata)
	}
}
