package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// rpc posts one JSON-RPC request to a path and decodes the response.
func rpcAt(t *testing.T, server *Server, path, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	return decoded
}

// rpc posts one JSON-RPC request to the multi-bank mount and decodes the response.
func rpc(t *testing.T, server *Server, body string) map[string]any {
	t.Helper()
	return rpcAt(t, server, "/mcp", body)
}

// The handshake names the reference server and its release version.
func TestInitializeHandshake(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	result, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %v", got["result"])
	}
	if result["protocolVersion"] != protocolVersion {
		t.Fatalf("protocolVersion = %v, want %q", result["protocolVersion"], protocolVersion)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != "hindsight-mcp-server" {
		t.Fatalf("serverInfo.name = %v, want hindsight-mcp-server", info["name"])
	}
	if info["version"] != serverVersion {
		t.Fatalf("serverInfo.version = %v, want %q", info["version"], serverVersion)
	}
}

// A client that asks for a revision the server speaks is answered with it.
func TestInitializeNegotiatesProtocolVersion(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2024-11-05"}}`)
	result, _ := got["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v, want the requested 2024-11-05", result["protocolVersion"])
	}
	// An unknown revision falls back to the server's own.
	got = rpc(t, newServer(t), `{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"1999-01-01"}}`)
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
		"params":{"name":"retain","arguments":{"content":"Alice works at Google."}}}`
	result, _ := rpcAt(t, server, "/mcp/b1", body)["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("single-bank retain reported an error: %v", result)
	}
	listed, _ := rpcAt(t, server, "/mcp/b1", `{"jsonrpc":"2.0","id":3,"method":"tools/call",
		"params":{"name":"list_memories","arguments":{}}}`)["result"].(map[string]any)
	content, _ := listed["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "Alice works at Google.") {
		t.Fatalf("single-bank list_memories payload = %s, want the retained text", text)
	}
}

// A tools/call runs the tool and returns its payload as text content.
func TestToolsCallRunsRetainAndRecall(t *testing.T) {
	server := newServer(t)
	retain := rpc(t, server, `{"jsonrpc":"2.0","id":3,"method":"tools/call",
		"params":{"name":"retain","arguments":{"bank_id":"b1","content":"the code is 4417"}}}`)
	result, _ := retain["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("retain reported an error: %v", result)
	}

	recall := rpc(t, server, `{"jsonrpc":"2.0","id":4,"method":"tools/call",
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

// reflect reads its question from the reference's query argument.
func TestReflectUsesQueryArgument(t *testing.T) {
	server := newServer(t)
	callTool(t, server, "1", "retain", `{"bank_id":"b1","content":"Alice works at Google."}`)
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
	if int(rpcError["code"].(float64)) != codeMethodNotFound {
		t.Fatalf("code = %v, want %d", rpcError["code"], codeMethodNotFound)
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

// A notification carries no id and is answered with an empty body.
func TestNotificationIsNotAnswered(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(
		`{"jsonrpc":"2.0","method":"tools/list"}`))
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, req)
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %s, want empty", rec.Body.String())
	}
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
	// Seed one memory so the read tools have something to return.
	if result := callTool(t, server, "1", "retain", `{"bank_id":"b1","content":"Alice works at Google."}`); result["isError"] == true {
		t.Fatalf("retain failed: %v", result)
	}
	created := callTool(t, server, "1", "get_mental_model", `{"bank_id":"b1","mental_model_id":"missing"}`)
	if created["isError"] != true {
		t.Fatalf("get_mental_model on a missing model should error, got %v", created)
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
	callTool(t, server, "1", "retain", `{"bank_id":"b1","content":"Alice works at Google."}`)

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
	got := rpc(t, server, `{"jsonrpc":"1.0","id":1,"method":"initialize"}`)
	rpcError, _ := got["error"].(map[string]any)
	if rpcError == nil || int(rpcError["code"].(float64)) != codeInvalidRequest {
		t.Fatalf("wrong version error = %v, want code %d", got["error"], codeInvalidRequest)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("non-JSON body response = %s", rec.Body.String())
	}
	errObj, _ := decoded["error"].(map[string]any)
	if errObj == nil || int(errObj["code"].(float64)) != codeParseError {
		t.Fatalf("parse error = %v, want code %d", decoded["error"], codeParseError)
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

// The transport applies the reference's Accept handling: the two media types a
// response may carry are supplied when the caller's Accept does not ask for an
// event stream, so a missing header and a JSON-only header are both served. A
// request that asks for an event stream while refusing JSON is refused before
// it is dispatched, with the transport's exact error and a null id.
func TestPostAcceptHeaderRequirement(t *testing.T) {
	const body = `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	for name, accept := range map[string]string{
		"missing":  "",
		"json":     "application/json",
		"both":     "application/json, text/event-stream",
		"wildcard": "*/*",
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
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
	if decoded.JSONRPC != "2.0" || string(decoded.ID) != "null" {
		t.Fatalf("406 envelope must carry a null id: %s", rec.Body.String())
	}
}

// The initialize handshake establishes a session, and a later call echoes the
// identifier it presents.
func TestInitializeCarriesSessionHeader(t *testing.T) {
	server := newServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
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

	init, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
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

	init, err := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
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

// initializeSession runs the handshake and returns the session it establishes.
func initializeSession(t *testing.T, server *Server) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	session := rec.Header().Get(mcpSessionHeader)
	if session == "" {
		t.Fatal("initialize response must carry a session id")
	}
	return session
}
