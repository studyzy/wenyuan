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
	"errors"
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
// asks for one it does not speak: the newest revision it supports.
const protocolVersion = "2025-11-25"

// supportedProtocolVersions are the MCP revisions this server accepts. A client
// that asks for one of these is answered with its own revision, which is the
// negotiation the protocol requires.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
	"2025-11-25": true,
}

// transportErrorID is the id the transport stamps on an error envelope whose
// request could not be identified, in place of the JSON-RPC null.
const transportErrorID = "server-error"

// jsonRPCVersion is the only JSON-RPC version the protocol uses.
const jsonRPCVersion = "2.0"

// Server answers MCP requests over one store.
type Server struct {
	store storage.Store

	// auditor, when non-nil, records auditable tool calls.
	auditor Auditor

	// multiBank and singleBank are the session registries of the two MCP apps
	// the reference runs: the multi-bank one behind /mcp and the single-bank
	// one behind /mcp/{bank_id}. Each app keeps its own sessions — a session
	// established on one is unknown to the other — because the reference gives
	// each its own session manager.
	multiBank  *sessionRegistry
	singleBank *sessionRegistry

	// enabledTools, when non-nil, is the server-level tool allowlist: every
	// tool outside it is hidden from tools/list and refused if called. Nil
	// means every tool, which is the reference's default.
	enabledTools map[string]bool
	// instructions is appended to the retain and recall tool descriptions, the
	// reference's WENYUAN_API_MCP_INSTRUCTIONS.
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
	return &Server{
		store:      store,
		auditor:    auditor,
		multiBank:  newSessionRegistry(),
		singleBank: newSessionRegistry(),
	}
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

// advertisedTool reports whether a name is one of the tools the mount's roster
// actually exposes: the reference roster for the mount, narrowed by the
// configured allowlist. A name outside that surface — one it never registered,
// or one the allowlist removed — is answered the way the reference answers an
// unknown tool, because the reference removes a disallowed tool from the
// surface entirely.
func (s *Server) advertisedTool(ctx context.Context, name string) bool {
	for _, def := range s.advertisedTools(mountBank(ctx) == "") {
		if def.Name == name {
			return true
		}
	}
	return false
}

// mcpSessionHeader is the header that carries the session a client establishes
// at initialize and presents on later requests, per the MCP StreamableHTTP
// transport.
const mcpSessionHeader = "Mcp-Session-Id"

// initializedNotification is the notification a client sends once the handshake
// completes. The transport treats its arrival as completing the handshake for a
// session whose initialize never completed — a client that opened one without
// the handshake and then announced itself.
const initializedNotification = "notifications/initialized"

// sseKeepAlive is how often an open event stream emits a comment, so an idle
// connection is not dropped by an intermediary that sees no traffic.
const sseKeepAlive = 15 * time.Second

// session is one live session's state. The reference serves a session only
// after its handshake: until an initialize request — or the client's initialized
// notification — arrives, it answers only the handshake and ping, and refuses
// every other request as a parameter-validation failure.
type liveSession struct {
	initialized bool
}

// sessionRegistry tracks the sessions one MCP app has established. A session is
// either live or terminated; an identifier in neither set names a session the
// app never opened, which the reference reports differently from one it has
// ended.
type sessionRegistry struct {
	mu         sync.Mutex
	active     map[string]*liveSession
	terminated map[string]struct{}
}

// newSessionRegistry builds an empty registry for one MCP app.
func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{
		active:     make(map[string]*liveSession),
		terminated: make(map[string]struct{}),
	}
}

// mint records a fresh session — one whose handshake has not yet completed —
// and returns its identifier and state. A failed random read returns empty,
// which leaves the response without a session header rather than failing the
// handshake.
func (r *sessionRegistry) mint() (string, *liveSession) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", nil
	}
	id := hex.EncodeToString(buf)
	live := &liveSession{}
	r.mu.Lock()
	r.active[id] = live
	r.mu.Unlock()
	return id, live
}

// lookup returns the live session an identifier names, or reports that the app
// terminated it. A nil session with no termination means the app never opened
// it.
func (r *sessionRegistry) lookup(id string) (*liveSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if live, ok := r.active[id]; ok {
		return live, false
	}
	_, ok := r.terminated[id]
	return nil, ok
}

// initialize marks a live session's handshake complete.
func (r *sessionRegistry) initialize(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if live, ok := r.active[id]; ok {
		live.initialized = true
	}
}

// terminate ends a live session: it moves to the terminated set so a later
// request presenting it is answered as a session that has gone away rather than
// as one that never existed.
func (r *sessionRegistry) terminate(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[id]; ok {
		delete(r.active, id)
		r.terminated[id] = struct{}{}
	}
}

// registryFor picks the session registry of the MCP app a request reaches: the
// single-bank app when the path fixes a bank, the multi-bank one otherwise.
// This mirrors the reference's choice between its two apps, which decides which
// set of sessions the request is validated against.
func (s *Server) registryFor(r *http.Request) *sessionRegistry {
	if requestBank(r) != "" {
		return s.singleBank
	}
	return s.multiBank
}

// presentedSession returns the session identifier a request carries and whether
// it carries the header at all. The distinction matters: a header present but
// empty names a session the app does not know, while an absent header is a
// request that names no session.
func presentedSession(r *http.Request) (string, bool) {
	values, ok := r.Header[mcpSessionHeader]
	if !ok {
		return "", false
	}
	if len(values) == 0 {
		return "", true
	}
	return values[0], true
}

// validSession reports whether id names a live session in either app. It exists
// for callers that inspect session state after the fact, not for request
// handling, which decides per app.
func (s *Server) validSession(id string) bool {
	if id == "" {
		return false
	}
	for _, registry := range []*sessionRegistry{s.multiBank, s.singleBank} {
		if live, _ := registry.lookup(id); live != nil {
			return true
		}
	}
	return false
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

// rpcError is the JSON-RPC error object. Data carries the reference's empty
// detail string on the parameter-validation errors, which it always emits.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// invalidParamsError is the reference's parameter-validation failure: the same
// code and message it returns for an unknown method, a malformed initialize, or
// a tools/call with no name.
func invalidParamsError() *rpcError {
	return &rpcError{Code: codeInvalidParams, Message: "Invalid request parameters", Data: ""}
}

// The JSON-RPC error codes the protocol defines.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
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

// servePost answers one JSON-RPC request. It first validates the Content-Type
// header, then resolves the session the request presents, then validates the
// Accept header, decodes the call, and dispatches it — answering every JSON-RPC
// message as a Server-Sent Events frame, the framing the reference transport
// returns.
//
// Sessions are mandatory after the handshake, as the reference enforces: the
// handshake itself may arrive without one, every other call must present a live
// session, and a call that presents none, an unknown one, or one that has been
// terminated is refused before it reaches a tool. A request that names no
// session still opens one, exactly as the reference's session manager does: its
// fresh identifier rides on the response — including on a refusal — so a client
// that followed the handshake never notices the difference.
func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	// The transport validates the Content-Type before it touches the session,
	// as the reference's SDK does: a request with the wrong media type is
	// refused without a session established or named, so the refusal carries no
	// session header.
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writePlainText(w, http.StatusBadRequest, "Invalid Content-Type header")
		return
	}
	registry := s.registryFor(r)
	session, presented := presentedSession(r)
	var live *liveSession
	if presented {
		var terminated bool
		live, terminated = registry.lookup(session)
		if terminated {
			w.Header().Set(mcpSessionHeader, session)
			writeTransportError(w, http.StatusNotFound, codeInvalidRequest, "Not Found: Session has been terminated")
			return
		}
		if live == nil {
			writeTransportError(w, http.StatusNotFound, codeInvalidRequest, "Session not found")
			return
		}
	} else {
		session, live = registry.mint()
	}
	if session != "" {
		w.Header().Set(mcpSessionHeader, session)
	}
	if !acceptablePost(r) {
		writeTransportError(w, http.StatusNotAcceptable, codeInvalidRequest,
			"Not Acceptable: Client must accept both application/json and text/event-stream")
		return
	}
	var call request
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		writeTransportError(w, http.StatusBadRequest, codeParseError, "Parse error: "+parseErrorMessage(r))
		return
	}
	if call.JSONRPC != jsonRPCVersion {
		writeTransportError(w, http.StatusBadRequest, codeInvalidParams, "Invalid request parameters")
		return
	}
	// Only the handshake — and only as a request, since a notification is no
	// request — may open a session: every other call has to present one, which
	// the transport reports as a missing session rather than as an unsupported
	// method.
	if !presented && (call.Method != "initialize" || len(call.ID) == 0) {
		writeTransportError(w, http.StatusBadRequest, codeInvalidRequest, "Bad Request: Missing session ID")
		return
	}
	// A notification is a well-formed message with no id; the transport accepts
	// it without answering. The client's own initialized notification is where a
	// handshake that skipped initialize completes.
	if len(call.ID) == 0 {
		if call.Method == initializedNotification {
			registry.initialize(session)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	// Until the handshake completes, a session answers only the handshake and
	// ping; every other request fails validation, exactly as the reference's
	// session reports it.
	if live != nil && !live.initialized && call.Method != "initialize" && call.Method != "ping" {
		writeSSE(w, errorResponse(call.ID, invalidParamsError()))
		return
	}
	if call.Method == "initialize" {
		result, rpcErr := s.initialize(call.Params)
		if rpcErr != nil {
			writeSSE(w, errorResponse(call.ID, rpcErr))
			return
		}
		registry.initialize(session)
		writeSSE(w, response{JSONRPC: jsonRPCVersion, ID: call.ID, Result: result})
		return
	}
	mount, bank := resolveBanks(r)
	ctx := withBanks(r.Context(), mount, bank)
	result, rpcErr := s.dispatch(ctx, call)
	if rpcErr != nil {
		writeSSE(w, errorResponse(call.ID, rpcErr))
		return
	}
	writeSSE(w, response{JSONRPC: jsonRPCVersion, ID: call.ID, Result: result})
}

// isJSONContentType reports whether the request declares the JSON media type
// the transport requires. An empty Content-Type is refused, as is any non-JSON
// type; parameters such as charset are ignored.
func isJSONContentType(header string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(header, ";", 2)[0]))
	return mediaType == "application/json"
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
	live, terminated := s.registryFor(r).lookup(session)
	if terminated {
		w.Header().Set(mcpSessionHeader, session)
		writeTransportError(w, http.StatusNotFound, codeInvalidRequest, "Not Found: Session has been terminated")
		return
	}
	if live == nil {
		writeTransportError(w, http.StatusNotFound, codeInvalidRequest, "Session not found")
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
// session cannot terminate anything, which the transport reports as a missing
// session rather than as an unsupported method; a session it does not know is
// a not-found, and one it already ended is reported as terminated.
func (s *Server) serveDelete(w http.ResponseWriter, r *http.Request) {
	registry := s.registryFor(r)
	session, presented := presentedSession(r)
	if !presented {
		// A DELETE without a session still opens one, exactly as the reference's
		// manager does, and then refuses to terminate a session the request did
		// not name.
		if minted, _ := registry.mint(); minted != "" {
			w.Header().Set(mcpSessionHeader, minted)
		}
		writeTransportError(w, http.StatusBadRequest, codeInvalidRequest, "Bad Request: Missing session ID")
		return
	}
	live, terminated := registry.lookup(session)
	if terminated {
		w.Header().Set(mcpSessionHeader, session)
		writeTransportError(w, http.StatusNotFound, codeInvalidRequest, "Not Found: Session has been terminated")
		return
	}
	if live == nil {
		writeTransportError(w, http.StatusNotFound, codeInvalidRequest, "Session not found")
		return
	}
	registry.terminate(session)
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

// writeSSE writes one JSON-RPC message as a single Server-Sent Events frame,
// the framing the reference transport returns for every answered POST: an
// `event: message` line, the JSON payload on a `data:` line, and a blank line
// terminating the frame. The response is streamed rather than buffered, so a
// client that reads events incrementally gets the message as soon as it is
// written.
func writeSSE(w http.ResponseWriter, payload any) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	w.WriteHeader(http.StatusOK)
	encoded, err := json.Marshal(payload)
	if err != nil {
		// The payload is built from decoded values, so this cannot fail in
		// practice; if it does, a bare error frame is still parseable by the
		// client rather than leaving the stream open with nothing on it.
		encoded, _ = json.Marshal(errorResponse(nil, invalidParamsError()))
	}
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", encoded)
}

// parseErrorMessage renders why the body could not be decoded the way the
// reference does. Its transport hands the bytes to CPython's json module, so a
// malformed body is reported with that module's sentence — "Expecting value:
// line 1 column 1 (char 0)" — rather than the Go decoder's wording. A body the
// Go decoder rejects but CPython accepts (the NaN and Infinity literals) or one
// already consumed keeps the Go decoder's own message.
func parseErrorMessage(r *http.Request) string {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "EOF"
	}
	if text, ok := httpx.PythonJSONDecodeError(body); ok {
		return text
	}
	return "EOF"
}

// writeTransportError emits the transport's JSON-RPC error envelope for a
// transport-level failure, which carries the status the transport returned and
// the "server-error" id the protocol reserves for an error whose request could
// not be identified.
func writeTransportError(w http.ResponseWriter, status, code int, message string) {
	writeJSON(w, status, errorEnvelope(code, message))
}

// writePlainText writes a bare-text transport response, the shape the reference
// returns for a rejected Content-Type: the body carries the message and the
// response declares no media type, so a client reads it as the transport's own
// complaint rather than as a payload in a format it asked for.
func writePlainText(w http.ResponseWriter, status int, message string) {
	// Declaring the header absent keeps net/http from sniffing a media type for
	// the body, which is what lets the refusal carry no Content-Type at all.
	w.Header()["Content-Type"] = nil
	w.WriteHeader(status)
	_, _ = io.WriteString(w, message)
}

// writeJSON writes a JSON body with an explicit status. It writes the encoded
// bytes without the newline an encoder would append, matching the reference's
// JSON responses byte for byte.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// transportErrorEnvelope is the transport's own error envelope, which carries
// the status the transport returned and the "server-error" id the protocol
// reserves for an error whose request could not be identified. The members are
// declared in the reference's order so the body reads the same.
type transportErrorEnvelope struct {
	JSONRPC string                        `json:"jsonrpc"`
	ID      string                        `json:"id"`
	Error   transportErrorEnvelopeMessage `json:"error"`
}

// transportErrorEnvelopeMessage is the error object of the transport envelope.
type transportErrorEnvelopeMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// errorEnvelope builds the transport error envelope with the "server-error" id.
func errorEnvelope(code int, message string) transportErrorEnvelope {
	return transportErrorEnvelope{
		JSONRPC: jsonRPCVersion,
		ID:      transportErrorID,
		Error:   transportErrorEnvelopeMessage{Code: code, Message: message},
	}
}

// dispatch routes one method to its handler. The protocol's own discovery
// methods are answered with empty surfaces — the reference server registers no
// resources, resource templates or prompts — and an unrecognised method is the
// transport's parameter-validation failure.
func (s *Server) dispatch(ctx context.Context, call request) (any, *rpcError) {
	switch call.Method {
	case "initialize":
		return s.initialize(call.Params)
	case "tools/list":
		return map[string]any{"tools": toolEntries(withInstructions(s.advertisedTools(mountBank(ctx) == ""), s.instructions))}, nil
	case "tools/call":
		return s.callTool(ctx, call.Params)
	case "ping":
		return map[string]any{}, nil
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	case "logging/setLevel":
		return map[string]any{}, nil
	default:
		return nil, invalidParamsError()
	}
}

// initialize answers the handshake with the server's capabilities and identity.
// It negotiates the protocol revision: a revision the server speaks is echoed,
// otherwise the server declares its newest. The request must carry the client's
// capabilities and identity, as the reference's handshake requires.
func (s *Server) initialize(params json.RawMessage) (any, *rpcError) {
	var asked struct {
		ProtocolVersion string          `json:"protocolVersion"`
		Capabilities    json.RawMessage `json:"capabilities"`
		ClientInfo      *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if len(params) == 0 || json.Unmarshal(params, &asked) != nil {
		return nil, invalidParamsError()
	}
	if len(asked.Capabilities) == 0 || asked.ClientInfo == nil ||
		asked.ClientInfo.Name == "" || asked.ClientInfo.Version == "" {
		return nil, invalidParamsError()
	}
	negotiated := protocolVersion
	if supportedProtocolVersions[asked.ProtocolVersion] {
		negotiated = asked.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": negotiated,
		"capabilities":    serverCapabilities(),
		"serverInfo":      map[string]any{"name": "wenyuan-mcp-server", "version": serverVersion},
	}, nil
}

// serverCapabilities is the capability set the reference server advertises:
// every capability the SDK's low-level server exposes with its tools-change
// notification enabled, plus the UI extension. The sets the server does not
// actually populate (resources, prompts) are still advertised, because the
// client's feature negotiation reads them.
func serverCapabilities() map[string]any {
	return map[string]any{
		"experimental": map[string]any{},
		"logging":      map[string]any{},
		"prompts":      map[string]any{"listChanged": true},
		"resources":    map[string]any{"subscribe": false, "listChanged": true},
		"tools":        map[string]any{"listChanged": true},
		"extensions":   map[string]any{"io.modelcontextprotocol/ui": map[string]any{}},
	}
}

// callTool runs one tool by name.
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, invalidParamsError()
	}
	if params.Name == "" {
		return nil, invalidParamsError()
	}
	if !s.advertisedTool(ctx, params.Name) {
		// The reference registers only its advertised tools, so a name outside
		// that surface — one it never registered, or one the allowlist removed —
		// is the same as calling something that does not exist.
		return toolError("Unknown tool: '" + params.Name + "'"), nil
	}
	// The transport repairs the two habits an LLM client has — an argument the
	// tool never declared, and a list or object sent as a JSON string — before
	// the tool sees them, which is what the reference's own wrapper does.
	arguments := normalizeToolArguments(s.toolArgumentSchema(ctx, params.Name), params.Arguments)
	payload, err := s.invokeAudited(ctx, params.Name, arguments)
	if err != nil {
		return toolFailure(params.Name, err), nil
	}
	return toolResult(params.Name, payload), nil
}

// toolFailure reports a tool failure as a tool result rather than a transport
// error, so the client sees which call failed instead of losing the session.
//
// The reference distinguishes two kinds. A call it could not attempt — a
// required argument missing from a request the transport validates before the
// tool runs — is an error result (isError true). Everything the tool itself
// failed at, including the ordinary negative answers the tools raise for a
// missing record, is a normal result whose payload carries the sentence: the
// two memory-write tools an error status object, recall an empty result list,
// and every other tool the error JSON.
func toolFailure(name string, err error) map[string]any {
	var invalid invalidArgumentError
	if errors.As(err, &invalid) {
		return toolError(invalid.Error())
	}
	message := err.Error()
	switch name {
	case "retain", "sync_retain":
		return objectToolResult(map[string]any{"status": "error", "message": message})
	case "recall":
		return textToolResult(errorText(message, jsonField{"results", []any{}}))
	case "list_banks":
		return textToolResult(errorText(message, jsonField{"banks", []any{}}))
	default:
		return textToolResult(errorText(message))
	}
}

// invalidArgumentError marks a failure the transport reports as an error result:
// the call could not be attempted because the request did not carry a value the
// tool's schema declares required, which the reference's own validation refuses
// before the tool runs.
type invalidArgumentError struct{ message string }

func (e invalidArgumentError) Error() string { return e.message }

// invalidArgument builds an invalidArgumentError.
func invalidArgument(message string) error { return invalidArgumentError{message: message} }

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

// mountBankKey carries the fixed bank a single-bank mount establishes.
// sessionBankKey carries the bank a tool uses when its arguments omit bank_id.
const (
	mountBankKey contextKey = iota
	sessionBankKey
)

// defaultMCPBank is the bank a request that names none is scoped to, the
// reference's DEFAULT_BANK_ID.
const defaultMCPBank = "default"

// withBanks records the mount's fixed bank — empty for the multi-bank mount —
// and the session bank a tool falls back to.
func withBanks(ctx context.Context, mount, session string) context.Context {
	if mount != "" {
		ctx = context.WithValue(ctx, mountBankKey, mount)
	}
	if session != "" {
		ctx = context.WithValue(ctx, sessionBankKey, session)
	}
	return ctx
}

// mountBank reads the fixed bank a single-bank mount set, or empty for a
// multi-bank request where every tool must carry its own bank_id.
func mountBank(ctx context.Context) string {
	bank, _ := ctx.Value(mountBankKey).(string)
	return bank
}

// sessionBank reads the bank a multi-bank request is scoped to: the X-Bank-Id
// header when it carries one, and the default bank otherwise.
func sessionBank(ctx context.Context) string {
	bank, _ := ctx.Value(sessionBankKey).(string)
	return bank
}

// resolveBanks picks the mount's fixed bank from the request path and the
// session bank from the header, falling back to the default bank. A path-based
// bank is both, which is what selects the single-bank tool surface.
func resolveBanks(r *http.Request) (mount, session string) {
	mount = requestBank(r)
	session = mount
	if session == "" {
		session = strings.TrimSpace(r.Header.Get("X-Bank-Id"))
	}
	if session == "" {
		session = defaultMCPBank
	}
	return mount, session
}

// errorResponse builds a JSON-RPC error response.
func errorResponse(id json.RawMessage, rpcErr *rpcError) response {
	return response{JSONRPC: jsonRPCVersion, ID: id, Error: rpcErr}
}
