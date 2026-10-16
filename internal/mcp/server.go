// Package mcp serves the Model Context Protocol over HTTP.
//
// An MCP client speaks JSON-RPC 2.0 to one endpoint. This package answers the
// three methods a client needs to start using the server — initialize,
// tools/list and tools/call — over the store the HTTP surface already uses, so
// an agent reaches the same memory through the same rules as a REST caller.
//
// The tool set is the reference roster, transcribed verbatim. Each tool maps
// onto a storage call, so the protocol layer adds no behavior of its own and
// stays free of the HTTP request shapes.
//
// The mount decides the mode. A server reached at /mcp is multi-bank: every
// tool takes an optional bank_id argument and the bank-management tools are
// present. A server reached at /mcp/{bank_id} is single-bank: the path fixes
// the bank, so bank_id disappears from every schema and the bank-management
// tools are withheld. Server.ServeHTTP reads the bank from the request path
// (a {bank_id} path value when the router registers one, otherwise the first
// path segment).
package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// protocolVersion is the MCP revision this server answers with when a client
// asks for one it does not speak.
const protocolVersion = "2025-06-18"

// supportedProtocolVersions are the MCP revisions this server accepts. A client
// that asks for one of these is answered with its own revision, which is the
// negotiation the protocol requires.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// jsonRPCVersion is the only JSON-RPC version the protocol uses.
const jsonRPCVersion = "2.0"

// Server answers MCP requests over one store.
type Server struct {
	store storage.Store

	// auditor, when non-nil, records auditable tool calls.
	auditor Auditor

	mu       sync.Mutex
	sessions map[string]struct{}

	// enabledTools, when non-nil, is the server-level tool allowlist: every
	// tool outside it is hidden from tools/list and refused if called. Nil
	// means every tool, which is the reference's default.
	enabledTools map[string]bool
	// instructions is appended to the retain and recall tool descriptions, the
	// reference's HINDSIGHT_API_MCP_INSTRUCTIONS.
	instructions string
}

// Auditor decides and records MCP audit entries. It is satisfied by
// *audit.Logger; the interface keeps the protocol layer free of the audit
// package's concrete type.
type Auditor interface {
	ShouldAudit(ctx context.Context, action, bankID string) bool
	Record(ctx context.Context, entry audit.Entry)
}

// New builds an MCP server over the store. A nil auditor disables auditing.
func New(store storage.Store, auditor Auditor) *Server {
	return &Server{store: store, auditor: auditor, sessions: make(map[string]struct{})}
}

// Options configure the advertised tool surface, so a deployment can narrow the
// roster and steer the two memory tools without changing the protocol layer.
type Options struct {
	// EnabledTools is the server-level allowlist. Empty means every tool.
	EnabledTools []string
	// Instructions is appended to the retain and recall descriptions.
	Instructions string
}

// WithOptions returns the server with its advertised surface configured.
func (s *Server) WithOptions(options Options) *Server {
	if len(options.EnabledTools) > 0 {
		allowed := make(map[string]bool, len(options.EnabledTools))
		for _, name := range options.EnabledTools {
			allowed[name] = true
		}
		s.enabledTools = allowed
	}
	s.instructions = options.Instructions
	return s
}

// advertisedTools is the roster this server presents: the reference roster,
// narrowed to the configured allowlist when there is one.
func (s *Server) advertisedTools(multiBank bool) []tool {
	defs := toolDefinitions(multiBank)
	if s.enabledTools == nil {
		return defs
	}
	filtered := make([]tool, 0, len(defs))
	for _, def := range defs {
		if s.enabledTools[def.Name] {
			filtered = append(filtered, def)
		}
	}
	return filtered
}

// toolAllowed reports whether a tool name is inside the configured allowlist. A
// server with no allowlist allows every name; a tool outside it is answered the
// way an unknown tool is, because the reference removes it from the surface
// entirely.
func (s *Server) toolAllowed(name string) bool {
	return s.enabledTools == nil || s.enabledTools[name]
}

// mcpSessionHeader is the header that carries the session a client establishes
// at initialize and presents on later requests, per the MCP StreamableHTTP
// transport.
const mcpSessionHeader = "Mcp-Session-Id"

// sseKeepAlive is how often an open event stream emits a comment, so an idle
// connection is not dropped by an intermediary that sees no traffic.
const sseKeepAlive = 15 * time.Second

// mintSession records a fresh session and returns its identifier. A failed
// random read returns empty, which leaves the response without a session header
// rather than failing the handshake.
func (s *Server) mintSession() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	id := hex.EncodeToString(buf)
	s.mu.Lock()
	s.sessions[id] = struct{}{}
	s.mu.Unlock()
	return id
}

// validSession reports whether id names a session this server established.
func (s *Server) validSession(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	return ok
}

// terminateSession forgets a session, so a later request presenting it is not
// found.
func (s *Server) terminateSession(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
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

// The JSON-RPC error codes the protocol defines.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// ServeHTTP serves the MCP StreamableHTTP transport: a POST carries a JSON-RPC
// call, a GET opens a server-to-client event stream, and a DELETE terminates a
// session.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.servePost(w, r)
	case http.MethodGet:
		s.serveGet(w, r)
	case http.MethodDelete:
		s.serveDelete(w, r)
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeTransportError(w, http.StatusMethodNotAllowed, codeInvalidRequest, "Method Not Allowed")
	}
}

// servePost answers one JSON-RPC request. The transport validates the Accept
// header first, then decodes and dispatches the call, and answers the
// initialize handshake with the session header the client presents thereafter.
func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	if !acceptablePost(r) {
		writeTransportError(w, http.StatusNotAcceptable, codeInvalidRequest,
			"Not Acceptable: Client must accept both application/json and text/event-stream")
		return
	}
	var call request
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		httpx.WriteJSONFor(w, r, http.StatusOK, errorResponse(nil, codeParseError, "request body is not valid JSON"))
		return
	}
	if call.JSONRPC != jsonRPCVersion {
		httpx.WriteJSONFor(w, r, http.StatusOK, errorResponse(call.ID, codeInvalidRequest, "jsonrpc must be \"2.0\""))
		return
	}
	// A notification is answered with nothing, which the transport expresses as
	// an empty body rather than a response object.
	if len(call.ID) == 0 {
		return
	}
	ctx := withDefaultBank(r.Context(), requestBank(r))
	result, rpcErr := s.dispatch(ctx, call)
	if rpcErr != nil {
		httpx.WriteJSONFor(w, r, http.StatusOK, errorResponse(call.ID, rpcErr.Code, rpcErr.Message))
		return
	}
	// The handshake establishes the session; a later call echoes the one it
	// presented so a stateful client keeps its identifier.
	if call.Method == "initialize" {
		if id := s.mintSession(); id != "" {
			w.Header().Set(mcpSessionHeader, id)
		}
	} else if session := r.Header.Get(mcpSessionHeader); session != "" {
		w.Header().Set(mcpSessionHeader, session)
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, response{JSONRPC: jsonRPCVersion, ID: call.ID, Result: result})
}

// serveGet opens the server-to-client event stream. A GET with no session is
// the pre-initialize probe some clients send; with nothing to stream it is
// acknowledged with an empty body so the client proceeds to the handshake. A
// GET that names a session must accept an event stream and is then held open
// with periodic keep-alives until the client disconnects.
func (s *Server) serveGet(w http.ResponseWriter, r *http.Request) {
	session := r.Header.Get(mcpSessionHeader)
	if session == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "{}")
		return
	}
	if !s.validSession(session) {
		writeTransportError(w, http.StatusNotFound, codeInvalidRequest, "Not Found: Invalid or expired session ID")
		return
	}
	if !acceptsEventStream(r) {
		writeTransportError(w, http.StatusNotAcceptable, codeInvalidRequest, "Not Acceptable: Client must accept text/event-stream")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeTransportError(w, http.StatusInternalServerError, codeInternalError, "event streaming is not supported")
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set(mcpSessionHeader, session)
	w.WriteHeader(http.StatusOK)
	// An opening comment confirms the stream before the first keep-alive, so a
	// client can treat the connection as live without waiting for a message.
	_, _ = io.WriteString(w, ": connected\r\n\r\n")
	flusher.Flush()
	ticker := time.NewTicker(sseKeepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// The keep-alive is the reference's own frame: a pinned comment
			// carrying the send time, separated by CRLF as the SSE writer it
			// inherits uses.
			if _, err := fmt.Fprintf(w, ": ping - %s\r\n\r\n", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// serveDelete terminates the session a client presents. A DELETE with no
// session cannot terminate anything and is refused, which is what tells the
// client session teardown is not supported.
func (s *Server) serveDelete(w http.ResponseWriter, r *http.Request) {
	session := r.Header.Get(mcpSessionHeader)
	if session == "" {
		writeTransportError(w, http.StatusMethodNotAllowed, codeInvalidRequest, "Method Not Allowed: Session termination not supported")
		return
	}
	if !s.validSession(session) {
		writeTransportError(w, http.StatusNotFound, codeInvalidRequest, "Not Found: Invalid or expired session ID")
		return
	}
	s.terminateSession(session)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(mcpSessionHeader, session)
	w.WriteHeader(http.StatusOK)
}

// acceptablePost reports whether a POST is one the transport can answer. The
// reference transport supplies the two media types a response may carry —
// application/json for the immediate payload and text/event-stream for a
// server-initiated message — whenever the caller's Accept does not ask for an
// event stream, so a client that sends only application/json, or nothing at
// all, is not refused a response it never said it could not read. Only a
// request that *does* ask for an event stream while refusing JSON is refused,
// because the response it demands cannot be produced.
func acceptablePost(r *http.Request) bool {
	header := normalizedAccept(r)
	hasJSON, hasSSE := acceptHas(header, "application/json"), acceptHas(header, "text/event-stream")
	return hasJSON && hasSSE
}

// acceptsEventStream reports whether the request can read the media type a GET
// event stream returns. It applies the same normalization as the POST check, so
// a GET whose Accept omits the event stream is supplied it rather than refused.
func acceptsEventStream(r *http.Request) bool {
	return acceptHas(normalizedAccept(r), "text/event-stream")
}

// normalizedAccept returns the Accept header the transport validates, applying
// the reference's normalization: both media types are supplied when the caller's
// header does not name text/event-stream.
func normalizedAccept(r *http.Request) string {
	header := r.Header.Get("Accept")
	if !strings.Contains(header, "text/event-stream") {
		return "application/json, text/event-stream"
	}
	return header
}

// acceptHas reports whether an Accept header names a media type. It understands
// the RFC 7231 wildcards the transport admits: `*/*` matches any type and
// `type/*` matches any subtype, so `*/*` satisfies both media types a response
// may use.
func acceptHas(header, media string) bool {
	for _, part := range strings.Split(header, ",") {
		mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case mediaType == media, mediaType == "*/*":
			return true
		case strings.HasSuffix(mediaType, "/*") &&
			strings.HasPrefix(media, strings.TrimSuffix(mediaType, "*")):
			return true
		}
	}
	return false
}

// writeTransportError emits the transport's JSON-RPC error envelope for a
// transport-level failure, which carries the status the transport returned and
// a null id, the value the protocol reserves for an error whose request could
// not be identified.
func writeTransportError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": jsonRPCVersion,
		"id":      nil,
		"error":   map[string]any{"code": code, "message": message},
	})
}

// dispatch routes one method to its handler.
func (s *Server) dispatch(ctx context.Context, call request) (any, *rpcError) {
	switch call.Method {
	case "initialize":
		return s.initialize(call.Params), nil
	case "tools/list":
		return map[string]any{"tools": withInstructions(s.advertisedTools(defaultBank(ctx) == ""), s.instructions)}, nil
	case "tools/call":
		return s.callTool(ctx, call.Params)
	case "ping":
		return map[string]any{}, nil
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "unknown method " + call.Method}
	}
}

// initialize answers the handshake with the server's capabilities and identity.
// It negotiates the protocol revision: a revision the server speaks is echoed,
// otherwise the server declares its own.
func (s *Server) initialize(params json.RawMessage) map[string]any {
	negotiated := protocolVersion
	var asked struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && json.Unmarshal(params, &asked) == nil && supportedProtocolVersions[asked.ProtocolVersion] {
		negotiated = asked.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": negotiated,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{"name": "hindsight-mcp-server", "version": serverVersion},
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
	if !s.toolAllowed(params.Name) {
		// The reference removes a disallowed tool from the surface, so calling
		// it is the same as calling something that does not exist.
		return toolError("unknown tool: " + params.Name), nil
	}
	payload, err := s.invokeAudited(ctx, params.Name, params.Arguments)
	if err != nil {
		// A tool failure is reported as a tool result, not a transport error,
		// so the client sees which call failed rather than losing the session.
		return toolError(err.Error()), nil
	}
	return toolResult(payload), nil
}

// requestBank is the bank a single-bank mount fixes, or empty for the
// multi-bank mount at /mcp. It reads the {bank_id} path value when the router
// registers a pattern for it, and otherwise takes the first segment after the
// namespace so the same handler serves every mount style.
func requestBank(r *http.Request) string {
	if bank := r.PathValue("bank_id"); bank != "" {
		return bank
	}
	rest := strings.TrimPrefix(r.URL.Path, httpx.NamespaceMCP)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return ""
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// contextKey is the private type for this package's context values.
type contextKey int

// defaultBankKey carries the single-bank mount's fixed bank through the request.
const defaultBankKey contextKey = iota

// withDefaultBank records the fixed bank for a single-bank request.
func withDefaultBank(ctx context.Context, bank string) context.Context {
	if bank == "" {
		return ctx
	}
	return context.WithValue(ctx, defaultBankKey, bank)
}

// defaultBank reads the fixed bank a single-bank mount set, or empty for a
// multi-bank request where every tool must carry its own bank_id.
func defaultBank(ctx context.Context) string {
	bank, _ := ctx.Value(defaultBankKey).(string)
	return bank
}

// errorResponse builds a JSON-RPC error response.
func errorResponse(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: message}}
}
