package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The text of a standard tool result is CPython's json.dumps(indent=2): two-space
// indentation, ensure_ascii on, and no HTML escaping. A tool the reference
// registered with indent=None renders compactly with the module's default
// separators instead, which is what the acknowledgements use.
func TestToolResultRendersPythonJSON(t *testing.T) {
	payload := map[string]any{"b": "café & <tea>", "a": 1}
	if got, want := pythonJSONText(payload), "{\n  \"a\": 1,\n  \"b\": \"caf\\u00e9 & <tea>\"\n}"; got != want {
		t.Fatalf("pretty text = %q, want %q", got, want)
	}
	if got, want := string(compactToolText(map[string]any{"status": "cleared", "n": 2})),
		`{"n": 2, "status": "cleared"}`; got != want {
		t.Fatalf("compact text = %q, want %q", got, want)
	}
	if got, want := errorText("boom", jsonField{"items", []any{}}),
		`{"error": "boom", "items": []}`; got != want {
		t.Fatalf("error text = %q, want %q", got, want)
	}
}

// A record that is not there is a normal negative answer: the tool reports the
// reference's sentence in its payload with isError false, so a client reads it
// with the shape it expects instead of losing the call.
func TestNotFoundAnswersAreNormalResults(t *testing.T) {
	server := newServer(t)
	cases := []struct {
		tool string
		args string
		want string
	}{
		{"get_bank", `{"bank_id":"missing"}`, `{"error": "Bank 'missing' not found"}`},
		{"get_memory", `{"bank_id":"b1","memory_id":"11111111-1111-1111-1111-111111111111"}`,
			`{"error": "Memory '11111111-1111-1111-1111-111111111111' not found"}`},
		{"get_document", `{"bank_id":"b1","document_id":"missing"}`, `{"error": "Document 'missing' not found"}`},
		{"get_mental_model", `{"bank_id":"b1","mental_model_id":"missing"}`,
			`{"error": "Mental model 'missing' not found in bank 'b1'"}`},
		{"list_mental_models", `{"bank_id":"missing"}`,
			`{"error": "Operation validation failed: Bank 'missing' not found", "items": []}`},
	}
	for _, tc := range cases {
		result := callTool(t, server, "1", tc.tool, tc.args)
		if result["isError"] == true {
			t.Errorf("%s: reported an error result: %v", tc.tool, result)
			continue
		}
		if got := rawText(t, result); got != tc.want {
			t.Errorf("%s: text = %s, want %s", tc.tool, got, tc.want)
		}
	}
}

// A tool failure the transport could not attempt — a required argument missing
// — stays an error result, which is how the reference's own validation refuses
// such a call before the tool runs.
func TestArgumentFailuresStayErrorResults(t *testing.T) {
	result := callTool(t, newServer(t), "1", "create_bank", `{}`)
	if result["isError"] != true {
		t.Fatalf("create_bank without an id = %v, want an error result", result)
	}
	if got := rawText(t, result); got != "create_bank requires bank_id" {
		t.Fatalf("create_bank without an id = %q", got)
	}
}

// The parse error carries CPython's sentence, because that is the parser the
// reference's transport runs over the body, and the envelope names the reserved
// "server-error" id in the reference's member order.
func TestParseErrorIsTheCPythonSentence(t *testing.T) {
	server := newServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("x"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	const want = `{"jsonrpc":"2.0","id":"server-error","error":{"code":-32700,"message":"Parse error: Expecting value: line 1 column 1 (char 0)"}}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("parse error body = %s, want %s", got, want)
	}
}

// The single-operation read reports an unknown identifier as a normal result
// carrying status not_found, which is what the reference's engine returns.
func TestGetOperationReportsNotFound(t *testing.T) {
	const opID = "22222222-2222-2222-2222-222222222222"
	result := callTool(t, newServer(t), "1", "get_operation", `{"bank_id":"b1","operation_id":"`+opID+`"}`)
	if result["isError"] == true {
		t.Fatalf("unknown operation = %v, want a normal result", result)
	}
	payload := decodeTextPayload(t, result)
	if payload["status"] != "not_found" || payload["operation_id"] != opID || payload["id"] != opID {
		t.Fatalf("unknown operation payload = %v", payload)
	}
	if _, ok := payload["task_type"]; !ok {
		t.Fatalf("unknown operation payload = %v, want the engine's members", payload)
	}
}

// The bank statistics tool reports the engine's members, whose names differ from
// the HTTP summary's.
func TestBankStatsReportsEngineKeys(t *testing.T) {
	result := callTool(t, newServer(t), "1", "get_bank_stats", `{"bank_id":"b1"}`)
	if result["isError"] == true {
		t.Fatalf("get_bank_stats failed: %v", result)
	}
	payload := decodeTextPayload(t, result)
	for _, key := range []string{
		"bank_id", "node_counts", "link_counts", "link_counts_by_fact_type", "link_breakdown",
		"operations", "total_documents", "last_consolidated_at", "last_memory_write_at",
		"pending_consolidation", "failed_consolidation", "total_observations",
	} {
		if _, ok := payload[key]; !ok {
			t.Errorf("get_bank_stats payload is missing %q: %v", key, payload)
		}
	}
	if len(payload) != 12 {
		t.Errorf("get_bank_stats payload has %d members, want 12: %v", len(payload), payload)
	}
}

// The list tools carry the page bounds the engine reports, and the mental-model
// and directive lists carry the total the engine reports beside their items.
func TestListToolsCarryTheEngineEnvelope(t *testing.T) {
	server := newServer(t)
	memories := callTool(t, server, "1", "list_memories", `{"bank_id":"b1"}`)
	payload := decodeTextPayload(t, memories)
	for _, key := range []string{"items", "total", "limit", "offset"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("list_memories payload is missing %q: %v", key, payload)
		}
	}
	if payload["limit"] != float64(100) || payload["offset"] != float64(0) {
		t.Errorf("list_memories bounds = %v/%v, want 100/0", payload["limit"], payload["offset"])
	}
	models := callTool(t, server, "2", "list_mental_models", `{"bank_id":"b1"}`)
	payload = decodeTextPayload(t, models)
	if _, ok := payload["total"]; !ok {
		t.Errorf("list_mental_models payload is missing total: %v", payload)
	}
	if _, ok := payload["limit"]; ok {
		t.Errorf("list_mental_models payload carries an HTTP-only bound: %v", payload)
	}
}

// create_bank on a bank that already exists applies the supplied profile rather
// than surfacing the store's duplicate-key failure.
func TestCreateBankUpdatesAnExistingBank(t *testing.T) {
	server := newServer(t)
	result := callTool(t, server, "1", "create_bank", `{"bank_id":"b1","name":"Renamed","mission":"be terse"}`)
	if result["isError"] == true {
		t.Fatalf("create_bank on an existing bank failed: %v", result)
	}
	payload := decodeTextPayload(t, result)
	if payload["name"] != "Renamed" || payload["mission"] != "be terse" {
		t.Fatalf("create_bank profile = %v", payload)
	}
}

// A JSON text renderer round-trips: what the encoder writes parses back to the
// same value, so no member is lost to the escaping.
func TestPythonJSONTextRoundTrips(t *testing.T) {
	payload := map[string]any{"text": "日本語 & <emoji 😀>", "n": []any{1.5, nil, true}}
	encoded := pythonJSONText(payload)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatalf("decode %s: %v", encoded, err)
	}
	if decoded["text"] != payload["text"] {
		t.Fatalf("text round trip = %v, want %v", decoded["text"], payload["text"])
	}
	if !strings.Contains(encoded, `\u65e5`) || !strings.Contains(encoded, `\ud83d\ude00`) {
		t.Fatalf("non-ASCII was not escaped the way ensure_ascii escapes it: %s", encoded)
	}
}

// MCP is served only when the deployment enables it. With the switch off the
// reference mounts no MCP middleware at all, so every MCP spelling falls through
// to the framework's own not-found answer rather than reaching a tool surface;
// with it on the same spellings answer the handshake.
func TestMCPDisabledAnswersTheFrameworkNotFound(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		router := httpx.NewRouter(httpx.RouterOptions{
			EnabledMCP: enabled,
			MCPHandler: New(storage.NewMemoryStore("public"), nil),
		})
		service := router.Service()
		for _, path := range []string{"/mcp", "/mcp/", "/mcp/b1", "/mcp/b1/"} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18",`+
					`"capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			rec := httptest.NewRecorder()
			service.ServeHTTP(rec, req)
			if enabled {
				if rec.Code != http.StatusOK {
					t.Fatalf("%s with MCP on = %d %s", path, rec.Code, rec.Body.String())
				}
				continue
			}
			if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"detail":"Not Found"}` {
				t.Fatalf("%s with MCP off = %d %s", path, rec.Code, rec.Body.String())
			}
		}
	}
}

// A memory identifier the reference's own validation rejects is reported with
// its sentence rather than handed to the store.
func TestGetMemoryRejectsAMalformedID(t *testing.T) {
	result := callTool(t, newServer(t), "1", "get_memory", `{"bank_id":"b1","memory_id":"nope"}`)
	if result["isError"] == true {
		t.Fatalf("get_memory with a malformed id = %v, want a normal result", result)
	}
	if got, want := rawText(t, result), `{"error": "Invalid memory_id: 'nope' is not a valid UUID"}`; got != want {
		t.Fatalf("get_memory with a malformed id = %s, want %s", got, want)
	}
}
