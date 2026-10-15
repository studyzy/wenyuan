package httpx

import (
	"context"
	"net/http"
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
	And     []TagGroup
	Or      []TagGroup
	Not     []TagGroup
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
	root.Handle(NamespaceExt+"/", chain(stripped))
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
