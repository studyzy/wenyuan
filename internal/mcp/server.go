// Package mcp serves the Model Context Protocol over HTTP.
//
// An MCP client speaks JSON-RPC 2.0 to one endpoint. This package answers the
// three methods a client needs to start using the server — initialize,
// tools/list and tools/call — over the store the HTTP surface already uses, so
// an agent reaches the same memory through the same rules as a REST caller.
//
// The tool set is deliberately the core memory operations. Each tool maps onto
// a storage call, so the protocol layer adds no behavior of its own and stays
// free of the HTTP request shapes.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// protocolVersion is the MCP revision this server answers. A client that asks
// for a different revision is answered with this one, which the protocol
// allows: the server declares what it speaks and the client adapts.
const protocolVersion = "2025-06-18"

// jsonRPCVersion is the only JSON-RPC version the protocol uses.
const jsonRPCVersion = "2.0"

// Server answers MCP requests over one store.
type Server struct {
	store storage.Store
}

// New builds an MCP server over the store.
func New(store storage.Store) *Server {
	return &Server{store: store}
}

// request is one JSON-RPC call. A notification carries no id and is answered
// with nothing.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// response is one JSON-RPC result or error.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC method-not-found and invalid-params codes.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// ServeHTTP answers one JSON-RPC request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "the MCP endpoint accepts POST"))
		return
	}
	var call request
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		httpx.WriteJSONFor(w, r, http.StatusOK, errorResponse(nil, codeInvalidParams, "request body is not valid JSON"))
		return
	}
	if call.JSONRPC != jsonRPCVersion {
		httpx.WriteJSONFor(w, r, http.StatusOK, errorResponse(call.ID, codeInvalidParams, "jsonrpc must be \"2.0\""))
		return
	}
	// A notification is answered with nothing, which the transport expresses as
	// an empty body rather than a response object.
	if len(call.ID) == 0 {
		return
	}
	result, rpcErr := s.dispatch(r.Context(), call)
	if rpcErr != nil {
		httpx.WriteJSONFor(w, r, http.StatusOK, errorResponse(call.ID, rpcErr.Code, rpcErr.Message))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, response{JSONRPC: jsonRPCVersion, ID: call.ID, Result: result})
}

// dispatch routes one method to its handler.
func (s *Server) dispatch(ctx context.Context, call request) (any, *rpcError) {
	switch call.Method {
	case "initialize":
		return s.initialize(), nil
	case "tools/list":
		return map[string]any{"tools": toolDefinitions()}, nil
	case "tools/call":
		return s.callTool(ctx, call.Params)
	case "ping":
		return map[string]any{}, nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "unknown method " + call.Method}
	}
}

// initialize answers the handshake with the server's capabilities and identity.
func (s *Server) initialize() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{"name": "wenyuan", "version": serverVersion},
	}
}

// callTool runs one tool by name.
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call requires a name and arguments"}
	}
	payload, err := s.invoke(ctx, params.Name, params.Arguments)
	if err != nil {
		// A tool failure is reported as a tool result, not a transport error,
		// so the client sees which call failed rather than losing the session.
		return toolError(err.Error()), nil
	}
	return toolResult(payload), nil
}

// stringArgument reads a required string argument.
func stringArgument(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}

// intArgument reads an optional integer argument, defaulting when absent.
func intArgument(args map[string]any, name string, fallback int) int {
	switch value := args[name].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return fallback
	}
}

// errorResponse builds a JSON-RPC error response.
func errorResponse(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: message}}
}
