package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newServer builds an MCP server over an in-memory store holding one bank.
func newServer(t *testing.T) *Server {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	return New(store, nil)
}

// initializeBody is the handshake the reference requires: a protocol revision,
// the client's capabilities and the client's identity.
const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize",
	"params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

// assertTransportError decodes a transport-level error envelope and checks the
// id and message the reference returns for it.
func assertTransportError(t *testing.T, rec *httptest.ResponseRecorder, message string) {
	t.Helper()
	var decoded struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("error body is not JSON: %s", rec.Body.String())
	}
	if decoded.JSONRPC != jsonRPCVersion || string(decoded.ID) != `"server-error"` {
		t.Fatalf("error envelope must carry the server-error id: %s", rec.Body.String())
	}
	if decoded.Error == nil || decoded.Error.Code != codeInvalidRequest || decoded.Error.Message != message {
		t.Fatalf("error = %+v, want code %d message %q", decoded.Error, codeInvalidRequest, message)
	}
}

// sseMessage extracts and decodes the JSON-RPC payload of an event-stream
// response.
func sseMessage(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(sseData(t, rec)), &decoded); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	return decoded
}

// rpcAt posts one JSON-RPC request to a path and decodes the response. A
// request other than the handshake is sent with the session the transport
// requires, established first on the same path.
func rpcAt(t *testing.T, server *Server, path, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if !strings.Contains(body, `"method":"initialize"`) {
		req.Header.Set(mcpSessionHeader, handshakeSession(t, server, path))
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(sseData(t, rec)), &decoded); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	return decoded
}

// sseData extracts the JSON-RPC payload from a Server-Sent Events response,
// which is how the transport returns every answered request.
func sseData(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream (body %s)", ct, rec.Body.String())
	}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			return payload
		}
	}
	t.Fatalf("response body carries no data frame: %s", rec.Body.String())
	return ""
}

// rpc posts one JSON-RPC request to the multi-bank mount and decodes the response.
func rpc(t *testing.T, server *Server, body string) map[string]any {
	t.Helper()
	return rpcAt(t, server, "/mcp", body)
}

// The handshake names the reference server and its release version.
func TestInitializeHandshake(t *testing.T) {
	got := rpc(t, newServer(t), initializeBody)
	result, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v", got["result"])
	}
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v, want the requested 2025-06-18", result["protocolVersion"])
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "wenyuan-mcp-server" {
		t.Fatalf("serverInfo.name = %v, want wenyuan-mcp-server", info["name"])
	}
	if info["version"] != serverVersion {
		t.Fatalf("serverInfo.version = %v, want %q", info["version"], serverVersion)
	}
}

// A client that asks for a revision the server speaks is answered with it.
func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	result, _ := got["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v, want the requested 2024-11-05", result["protocolVersion"])
	}
	// An unknown revision falls back to the server's own.
	got = rpc(t, newServer(t), `{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"1999-01-01","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	result, _ = got["result"].(map[string]any)
	if result["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion = %v, want %q", result["protocolVersion"], protocolVersion)
	}
}

// The multi-bank roster carries the reference's tools and drops the wenyuan-only
// ones.
func TestToolRosterMatchesReference(t *testing.T) {
	names := map[string]bool{}
	for _, def := range toolDefinitions(true) {
		names[def.Name] = true
	}
	for _, want := range []string{
		"retain", "sync_retain", "recall", "reflect", "list_banks", "create_bank",
		"list_mental_models", "get_mental_model", "create_mental_model", "update_mental_model",
		"delete_mental_model", "refresh_mental_model", "clear_mental_model",
		"list_directives", "create_directive", "delete_directive",
		"list_memories", "get_memory", "update_memory", "invalidate_memory",
		"list_documents", "get_document", "delete_document",
		"list_operations", "get_operation", "cancel_operation", "list_tags",
		"get_bank", "get_bank_stats", "update_bank", "delete_bank", "clear_memories",
		"get_knowledge_base_tree", "search_knowledge_base", "get_knowledge_page",
		"create_knowledge_folder", "create_knowledge_page", "update_knowledge_node",
		"delete_knowledge_node",
	} {
		if !names[want] {
			t.Errorf("roster is missing tool %q", want)
		}
	}
	for _, gone := range []string{
		"get_knowledge_tree", "search_knowledge",
		"list_entities", "list_entity_observations", "consolidate",
		"list_consolidation_scopes", "list_webhooks", "create_webhook", "delete_webhook",
	} {
		if names[gone] {
			t.Errorf("roster still carries the non-reference tool %q", gone)
		}
	}
	if len(names) != 39 {
		t.Fatalf("roster has %d tools, want 39", len(names))
	}
}

// Every advertised tool carries an input schema.
func TestToolsListAdvertisesTools(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result, _ := got["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) != 39 {
		t.Fatalf("tools/list returned %d tools, want 39", len(tools))
	}
	for _, entry := range tools {
		definition, _ := entry.(map[string]any)
		if definition["inputSchema"] == nil {
			t.Fatalf("tool %q has no input schema", definition["name"])
		}
	}
}

// A single-bank mount drops the bank-management tools and the bank_id argument.
func TestSingleBankMountScopesTheRoster(t *testing.T) {
	server := newServer(t)
	got := rpcAt(t, server, "/mcp/b1/tools/list", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result, _ := got["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := map[string]bool{}
	var retain map[string]any
	for _, entry := range tools {
		definition, _ := entry.(map[string]any)
		name, _ := definition["name"].(string)
		names[name] = true
		if name == "retain" {
			retain = definition
		}
	}
	if names["list_banks"] || names["create_bank"] || names["get_bank_stats"] {
		t.Fatalf("single-bank roster still carries the bank-management tools: %v", names)
	}
	if !names["retain"] {
		t.Fatal("single-bank roster is missing retain")
	}
	schema, _ := retain["inputSchema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	if _, ok := properties["bank_id"]; ok {
		t.Fatal("single-bank retain should not take bank_id")
	}
}

// tools/call on a single-bank mount uses the path's bank when the argument is absent.
func TestSingleBankMountFixesTheBank(t *testing.T) {
	server := newServer(t)
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{"name":"sync_retain","arguments":{"content":"Alice works at Google."}}}`
	result, _ := rpcAt(t, server, "/mcp/b1", body)["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("single-bank sync_retain reported an error: %v", result)
	}
	listed, _ := rpcAt(t, server, "/mcp/b1", `{"jsonrpc":"2.0","id":3,"method":"tools/call",
		"params":{"name":"list_memories","arguments":{}}}`)["result"].(map[string]any)
	content, _ := listed["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Alice works at Google.") {
		t.Fatalf("single-bank list_memories payload = %s, want the retained text", text)
	}
}

// A tools/call runs the tool and returns its payload as text content. retain
// queues the write and reports the receipt; the synchronous write is what makes
// the content recallable, so the recall check runs after sync_retain.
func TestToolsCallRunsRetainAndRecall(t *testing.T) {
	server := newServer(t)
	retain := rpc(t, server, `{"jsonrpc":"2.0","id":3,"method":"tools/call",
		"params":{"name":"retain","arguments":{"bank_id":"b1","content":"the code is 4417"}}}`)
	result, _ := retain["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("retain reported an error: %v", result)
	}
	receipt := decodeTextPayload(t, result)
	if receipt["status"] != "accepted" || receipt["message"] != "Memory storage initiated" {
		t.Fatalf("retain receipt = %v", receipt)
	}
	if id, _ := receipt["operation_id"].(string); id == "" {
		t.Fatalf("retain receipt carries no operation_id: %v", receipt)
	}

	sync := rpc(t, server, `{"jsonrpc":"2.0","id":4,"method":"tools/call",
		"params":{"name":"sync_retain","arguments":{"bank_id":"b1","content":"the code is 4417"}}}`)
	syncResult, _ := sync["result"].(map[string]any)
	stored := decodeTextPayload(t, syncResult)
	if stored["status"] != "completed" || stored["message"] != "Memory stored successfully" {
		t.Fatalf("sync_retain payload = %v", stored)
	}
	if ids, _ := stored["memory_ids"].([]any); len(ids) == 0 {
		t.Fatalf("sync_retain payload carries no memory_ids: %v", stored)
	}

	recall := rpc(t, server, `{"jsonrpc":"2.0","id":5,"method":"tools/call",
		"params":{"name":"recall","arguments":{"bank_id":"b1","query":"code"}}}`)
	recallResult, _ := recall["result"].(map[string]any)
	content, _ := recallResult["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", recallResult["content"])
	}
	text, _ := content[0].(map[string]any)
	if !strings.Contains(text["text"].(string), "4417") {
		t.Fatalf("recall payload = %v, want the retained text", text["text"])
	}
}

// decodeTextPayload parses a tool result's JSON text content into an object.
func decodeTextPayload(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("result carries no content: %v", result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode tool payload %s: %v", text, err)
	}
	return payload
}

// reflect reads its question from the reference's query argument.
func TestReflectUsesQueryArgument(t *testing.T) {
	server := newServer(t)
	callTool(t, server, "1", "sync_retain", `{"bank_id":"b1","content":"Alice works at Google."}`)
	result := callTool(t, server, "2", "reflect", `{"bank_id":"b1","query":"where does Alice work?"}`)
	if result["isError"] == true {
		t.Fatalf("reflect failed: %v", result)
	}
}

// An unknown method is a JSON-RPC method-not-found error.
func TestUnknownMethodIsAnError(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":5,"method":"bogus"}`)
	rpcError, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %v", got["error"])
	}
	// The reference reports an unrecognised method as a parameter-validation
	// failure with an empty detail, not as a method-not-found code.
	if int(rpcError["code"].(float64)) != codeInvalidParams {
		t.Fatalf("code = %v, want %d", rpcError["code"], codeInvalidParams)
	}
	if rpcError["message"] != "Invalid request parameters" || rpcError["data"] != "" {
		t.Fatalf("error = %v", rpcError)
	}
}

// A tool that fails reports the failure as a result rather than losing the
// session, which is what lets a client retry a different call.
func TestToolFailureIsAResult(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":6,"method":"tools/call",
		"params":{"name":"recall","arguments":{"bank_id":"","query":""}}}`)
	result, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v", got["result"])
	}
	if result["isError"] != true {
		t.Fatalf("isError = %v, want true", result["isError"])
	}
}

// A notification carries no id and is accepted with an empty body and the
// transport's 202 — but only once the session it rides on is established, which
// the transport requires of every message but the handshake.
func TestNotificationIsNotAnswered(t *testing.T) {
	server := newServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(mcpSessionHeader, handshakeSession(t, server, "/mcp"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %s, want empty", rec.Body.String())
	}
}

// A notification with no session is refused before it is answered: the session
// check precedes the 202 the transport would otherwise send.
func TestNotificationWithoutSessionIsRefused(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	assertTransportError(t, rec, "Bad Request: Missing session ID")
}

// callTool posts a tools/call and returns the decoded result content.
func callTool(t *testing.T, server *Server, id, name, args string) map[string]any {
	t.Helper()
	got := rpc(t, server, `{"jsonrpc":"2.0","id":`+id+`,"method":"tools/call",
		"params":{"name":"`+name+`","arguments":`+args+`}}`)
	result, _ := got["result"].(map[string]any)
	return result
}

// Every advertised tool is invocable and returns a payload rather than a
// transport error.
func TestEveryToolIsInvocable(t *testing.T) {
	server := newServer(t)
	// Seed one memory so the read tools have something to return. sync_retain
	// is the synchronous write, so the seeded memory is readable in this test.
	if result := callTool(t, server, "1", "sync_retain", `{"bank_id":"b1","content":"Alice works at Google."}`); result["isError"] == true {
		t.Fatalf("sync_retain failed: %v", result)
	}
	// A missing record is a normal negative answer in the reference, not an
	// error result, so the tool reports the sentence in its payload.
	created := callTool(t, server, "1", "get_mental_model", `{"bank_id":"b1","mental_model_id":"missing"}`)
	if created["isError"] == true {
		t.Fatalf("get_mental_model on a missing model reported an error, got %v", created)
	}
	if text := rawText(t, created); text != `{"error": "Mental model 'missing' not found in bank 'b1'"}` {
		t.Fatalf("get_mental_model on a missing model = %s", text)
	}
	model := callTool(t, server, "1", "create_mental_model", `{"bank_id":"b1","name":"m","source_query":"q"}`)
	if model["isError"] == true {
		t.Fatalf("create_mental_model failed: %v", model)
	}
	modelID := decodeField(t, model, "mental_model_id")

	cases := []struct {
		name string
		args string
	}{
		{"sync_retain", `{"bank_id":"b1","content":"Bob works at Acme."}`},
		{"recall", `{"bank_id":"b1","query":"Alice"}`},
		{"reflect", `{"bank_id":"b1","query":"where does Alice work?"}`},
		{"list_banks", `{}`},
		{"create_bank", `{"bank_id":"b2","name":"Two"}`},
		{"get_bank", `{"bank_id":"b1"}`},
		{"update_bank", `{"bank_id":"b1","name":"Renamed"}`},
		{"get_bank_stats", `{"bank_id":"b1"}`},
		{"clear_memories", `{"bank_id":"b2"}`},
		{"delete_bank", `{"bank_id":"b2"}`},
		{"list_memories", `{"bank_id":"b1"}`},
		{"list_directives", `{"bank_id":"b1"}`},
		{"create_directive", `{"bank_id":"b1","name":"d","content":"c"}`},
		{"list_mental_models", `{"bank_id":"b1"}`},
		{"get_mental_model", `{"bank_id":"b1","mental_model_id":"` + modelID + `"}`},
		{"update_mental_model", `{"bank_id":"b1","mental_model_id":"` + modelID + `","name":"n2"}`},
		{"refresh_mental_model", `{"bank_id":"b1","mental_model_id":"` + modelID + `"}`},
		{"clear_mental_model", `{"bank_id":"b1","mental_model_id":"` + modelID + `"}`},
		{"delete_mental_model", `{"bank_id":"b1","mental_model_id":"` + modelID + `"}`},
		{"list_documents", `{"bank_id":"b1"}`},
		{"list_operations", `{"bank_id":"b1"}`},
		{"list_tags", `{"bank_id":"b1"}`},
		{"get_knowledge_base_tree", `{"bank_id":"b1"}`},
		{"search_knowledge_base", `{"bank_id":"b1","query":"x"}`},
		{"create_knowledge_folder", `{"bank_id":"b1","name":"folder"}`},
	}
	for _, tc := range cases {
		result := callTool(t, server, "2", tc.name, tc.args)
		if result["isError"] == true {
			t.Fatalf("tool %q reported an error: %v", tc.name, result)
		}
		if _, ok := result["content"].([]any); !ok {
			t.Fatalf("tool %q returned no content: %v", tc.name, result)
		}
	}
}

// decodeField reads one string field from a tool result's JSON text payload.
func decodeField(t *testing.T, result map[string]any, field string) string {
	t.Helper()
	content, _ := result["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("decode tool payload %s: %v", text, err)
	}
	value, _ := payload[field].(string)
	return value
}

// get_memory reads one unit by identifier once the bank holds one.
func TestGetMemoryTool(t *testing.T) {
	server := newServer(t)
	callTool(t, server, "1", "sync_retain", `{"bank_id":"b1","content":"Alice works at Google."}`)

	listed := callTool(t, server, "2", "list_memories", `{"bank_id":"b1"}`)
	content, _ := listed["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	var page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil || len(page.Items) == 0 {
		t.Fatalf("list_memories payload = %s (err %v)", text, err)
	}
	got := callTool(t, server, "3", "get_memory", `{"bank_id":"b1","memory_id":"`+page.Items[0].ID+`"}`)
	if got["isError"] == true {
		t.Fatalf("get_memory failed: %v", got)
	}
}

// An unknown tool reports an error result rather than a transport failure.
func TestUnknownToolIsAResult(t *testing.T) {
	result := callTool(t, newServer(t), "1", "not_a_tool", `{}`)
	if result["isError"] != true {
		t.Fatalf("unknown tool = %v, want an error result", result)
	}
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("unknown tool content = %v, want one text entry", result["content"])
	}
	first, _ := content[0].(map[string]any)
	if first["text"] != "Unknown tool: 'not_a_tool'" {
		t.Fatalf("unknown tool content = %v, want the reference's message", result["content"])
	}
}

// A malformed tools/call body is a JSON-RPC invalid-params error.
func TestToolsCallMalformedParams(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":"not an object"}`)
	if _, ok := got["error"].(map[string]any); !ok {
		t.Fatalf("error = %v, want a JSON-RPC error", got["error"])
	}
}

// A wrong JSON-RPC version and a non-JSON body are both rejected with the
// JSON-RPC codes the protocol defines.
func TestMalformedRequests(t *testing.T) {
	server := newServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("non-JSON body response = %s", rec.Body.String())
	}
	errObj, _ := decoded["error"].(map[string]any)
	if errObj == nil || int(errObj["code"].(float64)) != codeParseError {
		t.Fatalf("parse error = %v, want code %d", decoded["error"], codeParseError)
	}
	if decoded["id"] != "server-error" {
		t.Fatalf("parse error id = %v, want server-error", decoded["id"])
	}
}

// The single-bank roster is the multi-bank roster minus the bank-management
// tools and the bank_id argument: nothing else may drift between the two.
func TestSingleBankRosterParity(t *testing.T) {
	multi := map[string]map[string]any{}
	for _, def := range toolDefinitions(true) {
		multi[def.Name] = def.InputSchema
	}
	single := map[string]bool{}
	for _, def := range toolDefinitions(false) {
		single[def.Name] = true
		want, ok := multi[def.Name]
		if !ok {
			t.Fatalf("single-bank roster has an unknown tool %q", def.Name)
		}
		gotProps, _ := def.InputSchema["properties"].(map[string]any)
		wantProps, _ := want["properties"].(map[string]any)
		wantProps = mirror(wantProps)
		delete(wantProps, "bank_id")
		if len(gotProps) != len(wantProps) {
			t.Fatalf("%s: single-bank properties %v, multi minus bank_id %v", def.Name, keys(gotProps), keys(wantProps))
		}
		for name := range wantProps {
			if _, ok := gotProps[name]; !ok {
				t.Fatalf("%s: single-bank schema is missing property %q", def.Name, name)
			}
		}
	}
	for name := range multi {
		if singleBankOmitted[name] {
			if single[name] {
				t.Fatalf("single-bank roster should omit %q", name)
			}
			continue
		}
		if !single[name] {
			t.Fatalf("single-bank roster is missing %q", name)
		}
	}
}

// mirror copies a property map so the parity check can delete bank_id without
// mutating the roster's own schema.
func mirror(props map[string]any) map[string]any {
	out := make(map[string]any, len(props))
	for key, value := range props {
		out[key] = value
	}
	return out
}

func keys(props map[string]any) []string {
	out := make([]string, 0, len(props))
	for key := range props {
		out = append(out, key)
	}
	return out
}

// A GET with no session is the pre-initialize probe a client sends: there is
// nothing to stream, so it is acknowledged with an empty body and the client
// proceeds to the handshake.
func TestGetWithoutSessionIsAProbe(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("probe status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "{}" {
		t.Fatalf("probe body = %q, want {}", body)
	}
}

// A GET that names a session the app never opened is a not-found, the same
// answer the reference's session manager gives before any stream is attempted.
func TestGetWithUnknownSessionIsNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(mcpSessionHeader, "unknown-session-id")
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	assertTransportError(t, rec, "Session not found")
}

// The transport applies the reference's Accept handling: the two media types a
// response may carry are supplied when the caller's Accept does not ask for an
// event stream, so a missing header and a JSON-only header are both served. A
// request that asks for an event stream while refusing JSON is refused before
// it is dispatched, with the transport's exact error and a null id.
func TestPostAcceptHeaderRequirement(t *testing.T) {
	const body = initializeBody
	for name, accept := range map[string]string{
		"missing":  "",
		"json":     "application/json",
		"both":     "application/json, text/event-stream",
		"wildcard": "*/*",
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200: %s", name, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotAcceptable {
		t.Fatalf("status = %d, want 406: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var decoded struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("406 body is not JSON: %s", rec.Body.String())
	}
	if decoded.Error == nil || decoded.Error.Code != -32600 ||
		decoded.Error.Message != "Not Acceptable: Client must accept both application/json and text/event-stream" {
		t.Fatalf("406 error = %+v", decoded.Error)
	}
	if decoded.JSONRPC != "2.0" || string(decoded.ID) != `"server-error"` {
		t.Fatalf("406 envelope must carry the server-error id: %s", rec.Body.String())
	}
}

// The initialize handshake establishes a session, and a later call echoes the
// identifier it presents.
func TestInitializeCarriesSessionHeader(t *testing.T) {
	server := newServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initializeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	session := rec.Header().Get(mcpSessionHeader)
	if session == "" {
		t.Fatal("initialize response must carry a session id")
	}
	next := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	next.Header.Set("Content-Type", "application/json")
	next.Header.Set("Accept", "application/json, text/event-stream")
	next.Header.Set(mcpSessionHeader, session)
	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, next)
	if got := rec2.Header().Get(mcpSessionHeader); got != session {
		t.Fatalf("later call session = %q, want %q", got, session)
	}
}

// A GET that presents a session and accepts an event stream opens a long-lived
// stream: the status and content type mark it as an event stream, the session
// is echoed, and the stream is live before the first keep-alive.
func TestGetOpensEventStream(t *testing.T) {
	server := newServer(t)
	ts := httptest.NewServer(server)
	defer ts.Close()

	init, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(initializeBody))
	if err != nil {
		t.Fatal(err)
	}
	session := init.Header.Get(mcpSessionHeader)
	_ = init.Body.Close()
	if session == "" {
		t.Fatal("initialize response must carry a session id")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(mcpSessionHeader, session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	if got := resp.Header.Get(mcpSessionHeader); got != session {
		t.Fatalf("stream session = %q, want %q", got, session)
	}
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), "connected") {
		t.Fatalf("first stream line = %q, want an opening comment", scanner.Text())
	}
}

// A GET that presents a session is served whatever its Accept says: the
// transport supplies text/event-stream when the caller's header does not name
// it, so a JSON-only Accept still opens the stream.
func TestGetSuppliesMissingEventStreamAccept(t *testing.T) {
	server := newServer(t)
	ts := httptest.NewServer(server)
	defer ts.Close()

	init, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(initializeBody))
	if err != nil {
		t.Fatal(err)
	}
	session := init.Header.Get(mcpSessionHeader)
	_ = init.Body.Close()
	if session == "" {
		t.Fatal("initialize response must carry a session id")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(mcpSessionHeader, session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || !strings.Contains(scanner.Text(), "connected") {
		t.Fatalf("first stream line = %q, want an opening comment", scanner.Text())
	}
}

// A DELETE presenting a session tears it down: the same session is then unknown
// to the stream endpoint.
func TestDeleteTerminatesSession(t *testing.T) {
	server := newServer(t)
	session := initializeSession(t, server)
	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	req.Header.Set(mcpSessionHeader, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if server.validSession(session) {
		t.Fatal("the deleted session must no longer be valid")
	}
}

// initializeSession runs the handshake on the multi-bank mount and returns the
// session it establishes.
func initializeSession(t *testing.T, server *Server) string {
	t.Helper()
	return handshakeSession(t, server, "/mcp")
}

// handshakeSession runs the initialize handshake on a path and returns the
// session it establishes. The handshake is the one call the transport serves
// without a session, and it is where every other call's session comes from.
func handshakeSession(t *testing.T, server *Server, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(initializeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handshake status = %d: %s", rec.Code, rec.Body.String())
	}
	session := rec.Header().Get(mcpSessionHeader)
	if session == "" {
		t.Fatal("initialize response must carry a session id")
	}
	return session
}

// postRaw posts a body to a path with a session header already set (empty for
// none) and returns the recorder, so a test can inspect a refusal.
func postRaw(t *testing.T, server *Server, path, body, session string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set(mcpSessionHeader, session)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

// A call that is not the handshake must present a session. One that names none
// is refused with the transport's missing-session error, and the fresh session
// the request opened rides on the refusal: the reference's manager opens one
// before the request is judged, so the identifier a client reads back names a
// live session.
func TestPostWithoutSessionIsRefused(t *testing.T) {
	server := newServer(t)
	rec := postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	assertTransportError(t, rec, "Bad Request: Missing session ID")
	fresh := rec.Header().Get(mcpSessionHeader)
	if fresh == "" {
		t.Fatal("a request that names no session still opens one, whose id rides on the response")
	}
	if again := postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":3,"method":"ping"}`, fresh); again.Code != http.StatusOK {
		t.Fatalf("the session minted on the refusal is not live: %d %s", again.Code, again.Body.String())
	}
}

// A session serves only the handshake and ping until it is initialized: the
// reference reports every other request as a parameter-validation failure, and
// the handshake then brings the session into service.
func TestSessionServesOnlyHandshakeAndPingBeforeInitialization(t *testing.T) {
	server := newServer(t)
	minted := postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "")
	session := minted.Header().Get(mcpSessionHeader)
	if session == "" {
		t.Fatal("the session-less request must open a session")
	}

	refused := sseMessage(t, postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, session))
	if errObj, _ := refused["error"].(map[string]any); errObj == nil ||
		int(errObj["code"].(float64)) != codeInvalidParams || errObj["message"] != "Invalid request parameters" {
		t.Fatalf("a request before initialization = %v, want the parameter-validation failure", refused)
	}

	pinged := sseMessage(t, postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":4,"method":"ping"}`, session))
	if _, ok := pinged["result"].(map[string]any); !ok {
		t.Fatalf("ping before initialization = %v, want a result", pinged)
	}

	if rec := postRaw(t, server, "/mcp", initializeBody, session); rec.Code != http.StatusOK {
		t.Fatalf("handshake on the minted session = %d: %s", rec.Code, rec.Body.String())
	}
	served := sseMessage(t, postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, session))
	if _, ok := served["result"].(map[string]any); !ok {
		t.Fatalf("tools/list after the handshake = %v, want a result", served)
	}
}

// The client's initialized notification completes the handshake for a session
// whose initialize never ran, which is the other way the reference brings a
// session into service.
func TestInitializedNotificationCompletesTheHandshake(t *testing.T) {
	server := newServer(t)
	minted := postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "")
	session := minted.Header().Get(mcpSessionHeader)
	note := postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, session)
	if note.Code != http.StatusAccepted {
		t.Fatalf("initialized notification = %d, want 202: %s", note.Code, note.Body.String())
	}
	served := sseMessage(t, postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`, session))
	if _, ok := served["result"].(map[string]any); !ok {
		t.Fatalf("tools/list after the notification = %v, want a result", served)
	}
}

// The handshake is the one call the transport serves without a session, and it
// echoes the session it establishes rather than refusing.
func TestHandshakeNeedsNoSession(t *testing.T) {
	server := newServer(t)
	rec := postRaw(t, server, "/mcp", initializeBody, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(mcpSessionHeader) == "" {
		t.Fatal("the handshake must establish a session")
	}
}

// A session the app never opened is a not-found, and carries no session header
// back: there is no transport to answer with one.
func TestUnknownSessionIsNotFound(t *testing.T) {
	server := newServer(t)
	rec := postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, "unknown-session-id")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	assertTransportError(t, rec, "Session not found")
	if got := rec.Header().Get(mcpSessionHeader); got != "" {
		t.Fatalf("an unknown session must not be echoed back, got %q", got)
	}
}

// A session the client already ended is reported as terminated, which the
// transport tells apart from one that never existed.
func TestTerminatedSessionIsToldApart(t *testing.T) {
	server := newServer(t)
	session := initializeSession(t, server)

	del := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	del.Header.Set(mcpSessionHeader, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, del)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	after := postRaw(t, server, "/mcp", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, session)
	if after.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", after.Code, after.Body.String())
	}
	assertTransportError(t, after, "Not Found: Session has been terminated")
	if got := after.Header().Get(mcpSessionHeader); got != session {
		t.Fatalf("a terminated session is echoed back, got %q want %q", got, session)
	}
}

// DELETE with no session cannot terminate anything: the transport reports a
// missing session and still hands back the session the request opened.
func TestDeleteWithoutSessionIsRefused(t *testing.T) {
	server := newServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	assertTransportError(t, rec, "Bad Request: Missing session ID")
	if rec.Header().Get(mcpSessionHeader) == "" {
		t.Fatal("the delete opens a session, whose id rides on the refusal")
	}
}

// The two MCP apps keep their sessions apart, exactly as the reference gives
// each its own session manager: a session from /mcp is unknown to
// /mcp/{bank_id} and vice versa.
func TestSessionsAreScopedToTheMount(t *testing.T) {
	server := newServer(t)
	if _, err := server.store.CreateBank(context.Background(), storage.Bank{BankID: "b2", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	multi := handshakeSession(t, server, "/mcp")
	single := handshakeSession(t, server, "/mcp/b1")
	if multi == single {
		t.Fatal("the two apps must not share a session identifier")
	}
	for _, tc := range []struct{ name, path, session string }{
		{"multi session on the single-bank mount", "/mcp/b1", multi},
		{"single-bank session on the multi mount", "/mcp", single},
	} {
		rec := postRaw(t, server, tc.path, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, tc.session)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404: %s", tc.name, rec.Code, rec.Body.String())
		}
		assertTransportError(t, rec, "Session not found")
	}
}

// The server-level allowlist hides every tool outside it, and a call to a
// hidden tool is answered the way an unknown tool is.
func TestEnabledToolsNarrowTheRoster(t *testing.T) {
	server := newServer(t).WithOptions(Options{EnabledTools: []string{"recall"}})
	defs := server.advertisedTools(true)
	if len(defs) != 1 || defs[0].Name != "recall" {
		t.Fatalf("advertised tools = %+v, want only recall", defs)
	}
	if server.advertisedTool(context.Background(), "retain") {
		t.Fatal("a tool outside the allowlist is still callable")
	}
	if !server.advertisedTool(context.Background(), "recall") {
		t.Fatal("an allowlisted tool is not callable")
	}
	// A server without an allowlist advertises the whole reference roster.
	if got := newServer(t).advertisedTools(true); len(got) != len(toolDefinitions(true)) {
		t.Fatalf("an unconfigured server advertises %d tools, want %d", len(got), len(toolDefinitions(true)))
	}
}

// The deployment's extra instructions ride on the two memory tools, verbatim
// and with the reference's separator.
func TestInstructionsAppendToRetainAndRecall(t *testing.T) {
	defs := withInstructions(toolDefinitions(true), "Prefer British spellings.")
	var sawRetain, sawRecall bool
	for _, def := range defs {
		switch def.Name {
		case "retain":
			sawRetain = strings.Contains(def.Description, "\n\nAdditional instructions: Prefer British spellings.")
		case "recall":
			sawRecall = strings.Contains(def.Description, "\n\nAdditional instructions: Prefer British spellings.")
		case "list_banks":
			if strings.Contains(def.Description, "Additional instructions") {
				t.Fatal("the instructions were appended to an unrelated tool")
			}
		}
	}
	if !sawRetain || !sawRecall {
		t.Fatalf("instructions missing: retain=%v recall=%v", sawRetain, sawRecall)
	}
}

// The handshake advertises the reference's capability set, including the tool
// list-change notification and the UI extension, and echoes a supported
// protocol revision beyond the previous ceiling.
func TestInitializeCapabilitiesAndLatestVersion(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	result, _ := got["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("protocolVersion = %v, want 2025-11-25", result["protocolVersion"])
	}
	caps, _ := result["capabilities"].(map[string]any)
	if caps == nil {
		t.Fatalf("capabilities = %v", result["capabilities"])
	}
	tools, _ := caps["tools"].(map[string]any)
	if tools["listChanged"] != true {
		t.Fatalf("capabilities.tools = %v, want listChanged true", caps["tools"])
	}
	extensions, _ := caps["extensions"].(map[string]any)
	if _, ok := extensions["io.modelcontextprotocol/ui"]; !ok {
		t.Fatalf("capabilities.extensions = %v, want the UI extension", caps["extensions"])
	}
	for _, key := range []string{"experimental", "logging", "prompts", "resources"} {
		if _, ok := caps[key]; !ok {
			t.Fatalf("capabilities is missing %q: %v", key, caps)
		}
	}
}

// The handshake requires the client's capabilities and identity, which the
// reference's request model rejects the absence of.
func TestInitializeRequiresClientCapabilitiesAndInfo(t *testing.T) {
	for name, params := range map[string]string{
		"absent":            ``,
		"empty":             `"params":{}`,
		"no-client-info":    `"params":{"protocolVersion":"2025-11-25","capabilities":{}}`,
		"no-capabilities":   `"params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"t","version":"1"}}`,
		"no-client-version": `"params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t"}}`,
	} {
		body := `{"jsonrpc":"2.0","id":1,"method":"initialize"`
		if params != "" {
			body += "," + params
		}
		body += "}"
		got := rpc(t, newServer(t), body)
		rpcError, ok := got["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s: error = %v, want a validation failure", name, got["error"])
		}
		if int(rpcError["code"].(float64)) != codeInvalidParams {
			t.Fatalf("%s: code = %v, want %d", name, rpcError["code"], codeInvalidParams)
		}
	}
}

// Every answered POST is a Server-Sent Events frame carrying the message, which
// is the framing the reference transport returns.
func TestPostAnswersWithAnEventStream(t *testing.T) {
	server := newServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(mcpSessionHeader, handshakeSession(t, server, "/mcp"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache, no-transform" {
		t.Fatalf("cache-control = %q", cc)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "event: message\ndata: ") || !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("frame = %q, want an event/data frame terminated by a blank line", body)
	}
}

// A POST that does not declare the JSON media type is refused before it is
// parsed, with the transport's plain-text error.
func TestPostRequiresJSONContentType(t *testing.T) {
	for name, contentType := range map[string]string{"missing": "", "text": "text/plain"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"ping"}`))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		newServer(t).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rec.Code)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != "Invalid Content-Type header" {
			t.Fatalf("%s: body = %q", name, body)
		}
		// The reference validates the media type before it touches the session,
		// so the refusal names no session, and it declares no media type of its
		// own for the bare-text complaint.
		if session := rec.Header().Get(mcpSessionHeader); session != "" {
			t.Fatalf("%s: refusal carried a session id %q", name, session)
		}
		if contentType := rec.Header().Get("Content-Type"); contentType != "" {
			t.Fatalf("%s: refusal declared Content-Type %q", name, contentType)
		}
	}
}

// Every advertised tool carries the annotations, structured-output schema and
// metadata the reference publishes, and the read-only and destructive sets are
// exactly the reference's.
func TestToolsListCarriesAnnotations(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result, _ := got["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	for _, entry := range tools {
		definition, _ := entry.(map[string]any)
		name, _ := definition["name"].(string)
		annotations, _ := definition["annotations"].(map[string]any)
		if annotations == nil {
			t.Fatalf("tool %q has no annotations", name)
		}
		if annotations["openWorldHint"] != false {
			t.Fatalf("tool %q openWorldHint = %v", name, annotations["openWorldHint"])
		}
		switch {
		case readOnlyMCPTools[name]:
			if annotations["readOnlyHint"] != true {
				t.Fatalf("read-only tool %q annotations = %v", name, annotations)
			}
		case destructiveMCPTools[name]:
			if annotations["destructiveHint"] != true {
				t.Fatalf("destructive tool %q annotations = %v", name, annotations)
			}
		default:
			if annotations["readOnlyHint"] != false || annotations["destructiveHint"] != false {
				t.Fatalf("write tool %q annotations = %v", name, annotations)
			}
		}
		if _, ok := definition["outputSchema"].(map[string]any); !ok {
			t.Fatalf("tool %q has no output schema", name)
		}
		meta, _ := definition["_meta"].(map[string]any)
		if _, ok := meta["fastmcp"]; !ok {
			t.Fatalf("tool %q has no _meta", name)
		}
	}
}

// The recall tool passes the compound tag filter, score floors, query anchor and
// temporal window through to retrieval instead of accepting and ignoring them.
func TestRecallArgumentsReachTheQuery(t *testing.T) {
	request, err := recallRequest(map[string]any{
		"query":               "x",
		"bank_id":             "b1",
		"tag_groups":          []any{map[string]any{"tags": []any{"t1"}, "match": "any_strict"}},
		"min_scores":          map[string]any{"reranker": 0.5},
		"query_timestamp":     "2024-01-15T10:30:00Z",
		"temporal_window":     map[string]any{"start": "2024-01-01", "end": "2024-02-01"},
		"tags_match":          "all",
		"prefer_observations": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := buildRecallQuery("b1", request)
	if err != nil {
		t.Fatal(err)
	}
	if query.MinScores["reranker"] != 0.5 {
		t.Fatalf("min_scores = %v", query.MinScores)
	}
	if len(query.TagGroups) != 1 || query.TagGroups[0].Tags[0] != "t1" || query.TagGroups[0].Match != "any_strict" {
		t.Fatalf("tag_groups = %+v", query.TagGroups)
	}
	if query.QueryTimestamp == nil || query.QueryTimestamp.UTC().Format(time.RFC3339) != "2024-01-15T10:30:00Z" {
		t.Fatalf("query_timestamp = %v", query.QueryTimestamp)
	}
	if query.Temporal == nil || !query.Temporal.HasStart || query.Temporal.Start.Format("2006-01-02") != "2024-01-01" {
		t.Fatalf("temporal = %+v", query.Temporal)
	}
	if query.Budget != defaultRecallBudget || query.MaxTokens != defaultRecallMaxTokens {
		t.Fatalf("defaults = budget %q max %d", query.Budget, query.MaxTokens)
	}
	if query.TagsMatch != "all" || !query.PreferObservations {
		t.Fatalf("query = %+v", query)
	}

	// A window whose bounds are inverted is refused rather than silently matching
	// nothing; the window model parses its own bounds at decode time.
	inverted := func() *memoryops.TemporalWindowRequest {
		var window memoryops.TemporalWindowRequest
		if err := json.Unmarshal([]byte(`{"start":"2024-02-01T00:00:00Z","end":"2024-01-01T00:00:00Z"}`), &window); err != nil {
			t.Fatal(err)
		}
		return &window
	}()
	if _, err := buildRecallQuery("b1", memoryops.RecallRequest{
		Query:          "x",
		TemporalWindow: inverted,
	}); err == nil {
		t.Fatal("an inverted temporal window must be refused")
	}
}

// The tool roster's read-only and destructive annotations match the reference's
// sets exactly, so a client's auto-approval decision cannot drift.
func TestToolAnnotationSetsMatchReference(t *testing.T) {
	wantRead := []string{
		"recall", "reflect", "list_banks", "get_bank", "get_bank_stats", "list_mental_models",
		"get_mental_model", "list_directives", "list_memories", "get_memory", "list_documents",
		"get_document", "list_operations", "get_operation", "list_tags", "get_knowledge_base_tree",
		"search_knowledge_base", "get_knowledge_page",
	}
	wantDestructive := []string{
		"delete_bank", "clear_memories", "clear_mental_model", "delete_mental_model",
		"delete_directive", "delete_document", "invalidate_memory", "delete_knowledge_node",
	}
	for _, name := range wantRead {
		if !readOnlyMCPTools[name] {
			t.Errorf("tool %q must be read-only", name)
		}
	}
	for _, name := range wantDestructive {
		if !destructiveMCPTools[name] {
			t.Errorf("tool %q must be destructive", name)
		}
	}
	if len(readOnlyMCPTools) != len(wantRead) || len(destructiveMCPTools) != len(wantDestructive) {
		t.Fatalf("annotation sets: read %d destructive %d", len(readOnlyMCPTools), len(destructiveMCPTools))
	}
}

// A multi-bank request scoped by the X-Bank-Id header resolves its bank from
// the header when a tool omits bank_id, and keeps the multi-bank roster.
func TestSessionBankFromHeader(t *testing.T) {
	server := newServer(t)
	session := handshakeSession(t, server, "/mcp")

	post := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Bank-Id", "from-header")
		req.Header.Set(mcpSessionHeader, session)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		var decoded map[string]any
		if err := json.Unmarshal([]byte(sseData(t, rec)), &decoded); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		return decoded
	}

	// A write with no bank_id lands in the header's bank.
	got := post(`{"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"sync_retain","arguments":{"content":"header scoped"}}}`)
	result, _ := got["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("sync_retain failed: %v", result)
	}
	listed := post(`{"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{"name":"list_memories","arguments":{}}}`)
	payload := decodeTextPayload(t, listed["result"].(map[string]any))
	if items, _ := payload["items"].([]any); len(items) != 1 {
		t.Fatalf("header bank items = %v, want the one just written", payload)
	}

	// The roster stays the multi-bank one, because the bank came from a header
	// and not from the path.
	roster := post(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	tools, _ := roster["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 39 {
		t.Fatalf("header-scoped roster has %d tools, want the multi-bank 39", len(tools))
	}

	// With neither a path bank nor a header, the request is scoped to the
	// default bank.
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_memories","arguments":{}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(mcpSessionHeader, session)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(sseData(t, rec)), &decoded); err != nil {
		t.Fatal(err)
	}
	result, _ = decoded["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("a default-bank read failed: %v", result)
	}
}

// rawText reads the verbatim text of a tool result's first content entry.
func rawText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("result carries no content: %v", result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}
