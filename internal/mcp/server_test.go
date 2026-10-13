package mcp

import (
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
	if _, err := store.CreateBank(context.Background(), storage.Bank{ID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	return New(store)
}

// rpc posts one JSON-RPC request and decodes the response.
func rpc(t *testing.T, server *Server, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
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

// The handshake names the protocol version and the server.
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
	if info["name"] != "wenyuan" {
		t.Fatalf("serverInfo = %v", info)
	}
}

// tools/list advertises the core memory tools with input schemas.
func TestToolsListAdvertisesTools(t *testing.T) {
	got := rpc(t, newServer(t), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	result, _ := got["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := map[string]bool{}
	for _, entry := range tools {
		definition, _ := entry.(map[string]any)
		name, _ := definition["name"].(string)
		names[name] = true
		if definition["inputSchema"] == nil {
			t.Fatalf("tool %q has no input schema", name)
		}
	}
	for _, want := range []string{"retain", "recall", "reflect", "list_banks"} {
		if !names[want] {
			t.Fatalf("tool %q is not advertised", want)
		}
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

	cases := []struct {
		name string
		args string
	}{
		{"recall", `{"bank_id":"b1","query":"Alice"}`},
		{"reflect", `{"bank_id":"b1","question":"where does Alice work?"}`},
		{"list_banks", `{}`},
		{"get_bank", `{"bank_id":"b1"}`},
		{"list_memories", `{"bank_id":"b1"}`},
		{"list_mental_models", `{"bank_id":"b1"}`},
		{"list_directives", `{"bank_id":"b1"}`},
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

// get_memory reads one unit by identifier once the bank holds one.
func TestGetMemoryTool(t *testing.T) {
	server := newServer(t)
	callTool(t, server, "1", "retain", `{"bank_id":"b1","content":"Alice works at Google."}`)

	listed := callTool(t, server, "2", "list_memories", `{"bank_id":"b1"}`)
	content, _ := listed["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	var page struct {
		Memories []struct {
			ID string `json:"id"`
		} `json:"memories"`
	}
	if err := json.Unmarshal([]byte(text), &page); err != nil || len(page.Memories) == 0 {
		t.Fatalf("list_memories payload = %s (err %v)", text, err)
	}
	got := callTool(t, server, "3", "get_memory", `{"bank_id":"b1","memory_id":"`+page.Memories[0].ID+`"}`)
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

// A wrong JSON-RPC version and a non-JSON body are both rejected.
func TestMalformedRequests(t *testing.T) {
	server := newServer(t)
	if got := rpc(t, server, `{"jsonrpc":"1.0","id":1,"method":"initialize"}`); got["error"] == nil {
		t.Fatal("a wrong jsonrpc version must be rejected")
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil || decoded["error"] == nil {
		t.Fatalf("non-JSON body response = %s", rec.Body.String())
	}
}

// A GET is refused, because the transport is POST-only.
func TestGetIsRefused(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("GET must be refused, got %d", rec.Code)
	}
}
