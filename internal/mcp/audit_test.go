package mcp

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Every auditable MCP tool's action is part of the deployment-wide
// enumeration, so the roster and the MCP set cannot drift apart.
func TestMCPAuditableActionsAreEnumerated(t *testing.T) {
	known := map[string]bool{}
	for _, action := range audit.Actions {
		known[action] = true
	}
	for name := range auditableMCPTools {
		if !known[name] {
			t.Errorf("MCP tool %q is not an enumerated action", name)
		}
	}
}

// An auditable tool call records one mcp-transport row; a non-auditable call
// records nothing.
func TestMCPAuditRecordsAuditableTools(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	server := New(store, audit.NewLogger(store, true, nil))

	rpc(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"recall","arguments":{"bank_id":"b1","query":"hello"}}}`)
	// list_banks is not on the auditable roster.
	rpc(t, server, `{"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{"name":"list_banks","arguments":{}}}`)

	page, err := store.ListAuditLog(context.Background(), storage.AuditQuery{BankID: "b1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("expected one audited row, got %d: %+v", len(page.Entries), page.Entries)
	}
	entry := page.Entries[0]
	if entry.Action != "recall" || entry.Transport != "mcp" {
		t.Fatalf("row = action %q transport %q, want recall/mcp", entry.Action, entry.Transport)
	}
	if entry.Request["query"] != "hello" {
		t.Errorf("request not recorded: %v", entry.Request)
	}
}

// With auditing off, an auditable tool call records nothing.
func TestMCPAuditDisabledProducesNoRows(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	server := New(store, audit.NewLogger(store, false, nil))
	rpc(t, server, `{"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"recall","arguments":{"bank_id":"b1","query":"hello"}}}`)

	page, err := store.ListAuditLog(context.Background(), storage.AuditQuery{BankID: "b1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 0 {
		t.Fatalf("expected no rows, got %+v", page.Entries)
	}
}
