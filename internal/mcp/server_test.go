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
