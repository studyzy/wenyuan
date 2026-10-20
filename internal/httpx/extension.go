package httpx

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Operation names the validator gates. They mirror the reference's operation
// names so a deployment's validator sees a stable vocabulary.
const (
	OperationRetain  = "retain"
	OperationRecall  = "recall"
	OperationReflect = "reflect"
)

// TagGroup is one node of a caller's resolved tag scope. It mirrors the storage
// filter shape so a scope can be AND-ed into any tag-scoped read without httpx
// depending on the storage area.
type TagGroup struct {
	And []TagGroup
	Or  []TagGroup
	// Not is the one child of a negation. The reference's TagGroupNot holds a
	// single node (engine/search/tags.py:333-337) and refuses a list, so this
	// carries one rather than a list.
	Not     *TagGroup
	Tag     string
	Tags    []string
	Resolve string
	Match   string
}

// OperationRequest identifies the operation a caller is attempting.
type OperationRequest struct {
	Operation string
	BankID    string
}

// OperationDecision is a validator's answer for one operation. A denial carries
// the reason shown to the caller and the HTTP status the denial is answered
// with; a zero status is answered as 403.
type OperationDecision struct {
	Allowed bool
	Reason  string
	Status  int
}

// OperationValidator is a deployment-provided gate for memory operations. It
// decides whether an operation is allowed and resolves the tag groups the
// caller is confined to. A host with no registered validator leaves every
// operation allowed and every scope empty, which is the unextended behaviour.
type OperationValidator interface {
	// ValidateOperation decides whether one operation is allowed.
	ValidateOperation(ctx context.Context, op OperationRequest) OperationDecision
	// ResolveTagScope returns the tag groups the caller is confined to within
	// the bank. An empty result confines the caller to nothing.
	ResolveTagScope(ctx context.Context, bankID string) []TagGroup
}

// RouteRegistrar is the minimal surface an area uses to register handlers.
type RouteRegistrar interface {
	Handle(method, pattern string, handler http.HandlerFunc)
}

// Extension is a deployment-provided router mounted under the fixed extension
// prefix. Extensions may add public routes without changing the core surface.
type Extension interface {
	// Mount registers the extension's routes on the extension registrar.
	Mount(r RouteRegistrar)
}

// ExtensionHost collects extensions registered with the application.
type ExtensionHost struct {
	mu         sync.Mutex
	extensions []Extension
	feature    map[string]bool
	validator  OperationValidator
	// passthrough is the operator's allowlist of request headers copied into
	// the extension context, lower-cased. Empty forwards nothing, which is the
	// reference's default and keeps a header out of extension code unless an
	// operator opted in.
	passthrough []string
}

// NewExtensionHost builds an empty extension host.
func NewExtensionHost() *ExtensionHost {
	return &ExtensionHost{feature: map[string]bool{}}
}

// Register adds an extension to the host.
func (h *ExtensionHost) Register(ext Extension) {
	if ext == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.extensions = append(h.extensions, ext)
}

// RegisterOperationValidator installs the deployment's operation validator.
// Registering nil is a no-op, so a deployment that registers nothing keeps the
// unextended behaviour. A later registration replaces an earlier one.
func (h *ExtensionHost) RegisterOperationValidator(v OperationValidator) {
	if h == nil || v == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.validator = v
}

// operationValidator returns the registered validator, or nil when none is set.
func (h *ExtensionHost) operationValidator() OperationValidator {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.validator
}

// EnableFeature records a deployment feature flag that extensions may key on.
func (h *ExtensionHost) EnableFeature(name string, enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.feature[name] = enabled
}

// FeatureEnabled reports whether a deployment feature flag is active.
func (h *ExtensionHost) FeatureEnabled(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.feature[name]
}

// SetPassthroughHeaders names the request headers copied into the extension
// context. The reference lower-cases its allowlist at configuration load; the
// normalisation is repeated here so matching stays case-insensitive for a
// caller that hands over the raw environment value. An empty list (the
// reference's default) forwards nothing.
func (h *ExtensionHost) SetPassthroughHeaders(headers []string) {
	if h == nil {
		return
	}
	normalized := make([]string, 0, len(headers))
	for _, name := range headers {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			normalized = append(normalized, name)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.passthrough = normalized
}

// passthroughHeaders returns the configured allowlist, or nil when unset.
func (h *ExtensionHost) passthroughHeaders() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.passthrough
}

// extraHeadersKey is the request-context key holding the forwarded headers.
type extraHeadersKey struct{}

// ExtraHeadersFrom returns the allowlisted request headers an extension may
// read, keyed by the lower-cased name, or nil when the deployment forwards
// none. The map is shared read-only across the request, so extension code must
// not mutate it.
func ExtraHeadersFrom(r *http.Request) map[string]string {
	headers, _ := r.Context().Value(extraHeadersKey{}).(map[string]string)
	return headers
}

// withExtraHeaders records the forwarded headers for this request. An empty
// result is left unrecorded so a request with no allowlisted header carries no
// context entry at all.
func withExtraHeaders(r *http.Request, headers map[string]string) *http.Request {
	if len(headers) == 0 {
		return r
	}
	return r.WithContext(contextWithValue(r.Context(), extraHeadersKey{}, headers))
}

// CollectPassthroughHeaders picks the allowlisted headers out of a request,
// keyed by the lower-cased name. An empty allowlist (the default) collects
// nothing, so extensions only ever see a header an operator opted into.
//
// A header sent more than once is dropped rather than resolved: these headers
// carry identity for the deployments that enable this, and there is no safe
// universal rule for choosing between copies (a proxy may append its trusted
// value before or after a client-supplied one). Dropping makes a duplicate a
// loud failure in the extension, which sees no header, instead of a silent
// choice between a real and a spoofed value.
func CollectPassthroughHeaders(header http.Header, allowlist []string) map[string]string {
	if len(allowlist) == 0 {
		return nil
	}
	wanted := make(map[string]bool, len(allowlist))
	for _, name := range allowlist {
		wanted[strings.ToLower(strings.TrimSpace(name))] = true
	}
	found := map[string][]string{}
	for name, values := range header {
		lower := strings.ToLower(name)
		if !wanted[lower] {
			continue
		}
		found[lower] = append(found[lower], values...)
	}
	collected := map[string]string{}
	for name, values := range found {
		if len(values) != 1 {
			continue
		}
		collected[name] = values[0]
	}
	if len(collected) == 0 {
		return nil
	}
	return collected
}

// PassthroughHeadersMiddleware records the allowlisted headers on the request
// context for downstream extension handlers. An empty allowlist leaves the
// request untouched, so behaviour is unchanged when the deployment opts out.
func PassthroughHeadersMiddleware(allowlist []string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, withExtraHeaders(r, CollectPassthroughHeaders(r.Header, allowlist)))
		})
	}
}

// MountAll registers every collected extension on the given registrar.
func (h *ExtensionHost) MountAll(r RouteRegistrar) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ext := range h.extensions {
		ext.Mount(r)
	}
}

// MountExtensions wires the extension namespace into the root mux using the
// same middleware chain as the core surface. The extension prefix is stripped
// before the extension router sees the request so extensions register paths
// relative to their own root.
func MountExtensions(root *http.ServeMux, host *ExtensionHost, chain Middleware) {
	extMux := http.NewServeMux()
	registrar := &muxRegistrar{mux: extMux}
	host.MountAll(registrar)
	stripped := http.StripPrefix(NamespaceExt, extMux)
	// The allowlisted headers are collected per request and placed on the
	// extension handler's context, which is where an extension reads them; the
	// reference does the same at its request-context boundary.
	mounted := chain(PassthroughHeadersMiddleware(host.passthroughHeaders())(stripped))
	root.Handle(NamespaceExt+"/", mounted)
}

type muxRegistrar struct {
	mux *http.ServeMux
	// record, when set, is told each registered path template so the router can
	// resolve the metrics endpoint label.
	record func(string)
}

func (m *muxRegistrar) Handle(method, pattern string, handler http.HandlerFunc) {
	if m.record != nil {
		m.record(pattern)
	}
	m.mux.HandleFunc(method+" "+pattern, handler)
}
