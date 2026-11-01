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

// ExtensionSelector is the optional capability of an extension that wants to
// know the class path the deployment selected it by. The host records the
// selector during composition and offers it to every extension before serving,
// so an extension registered under the configured class can read its own
// settings keyed by that name.
type ExtensionSelector interface {
	// SelectedAs is called once with the selector the deployment configured.
	SelectedAs(class string)
}

// ExtensionLifecycle is the optional lifecycle an extension may implement. The
// host asks for it by type assertion rather than putting the hooks on Extension
// itself, so an extension that needs no lifecycle keeps implementing one method.
//
// Start runs once, before the composed application is served, and its failure
// refuses startup — an extension that could not initialize must not be served
// half-initialized. Stop runs once, on the graceful shutdown path only; Serve
// never calls it when the process is torn down abruptly, and it is not called
// for a Start that failed.
type ExtensionLifecycle interface {
	Start() error
	Stop()
}

// ExtensionRootRouter is the optional capability of an extension that wants to
// register routes at the application root instead of under the extension
// prefix. The host asks for it by type assertion; an extension that does not
// implement it contributes only its Mounted routes.
type ExtensionRootRouter interface {
	// MountRoot registers routes on the root registrar. The patterns are the
	// application's own, so an extension can answer a path the core surface
	// leaves free.
	MountRoot(r RouteRegistrar)
}

// ExtensionRootMount is one extension's root contributions, in registration
// order. The composition root applies them to its own router.
type ExtensionRootMount struct {
	Extension Extension
	Router    ExtensionRootRouter
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
	// settings are the deployment's extension-only settings. They are inert
	// until an extension is registered and reads them; no core route sees them.
	settings map[string]string
	// selectedClass is the class path the deployment configured.
	selectedClass string
}

// NewExtensionHost builds an empty extension host.
func NewExtensionHost() *ExtensionHost {
	return &ExtensionHost{feature: map[string]bool{}}
}

// Register adds an extension to the host. An extension registered after the
// deployment's class was recorded is told the selector immediately, so the
// order a caller registers in does not decide whether it sees the selection.
func (h *ExtensionHost) Register(ext Extension) {
	if ext == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.extensions = append(h.extensions, ext)
	if h.selectedClass == "" {
		return
	}
	if selected, ok := ext.(ExtensionSelector); ok {
		selected.SelectedAs(h.selectedClass)
	}
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

// ImportGate returns the registered validator's import gate, or nil when no
// validator is registered or the one registered carries no import rule. A nil
// gate lets an import through untouched, which is the unextended behaviour.
// The capability is asked of the validator the host holds, so a deployment
// registers one validator and implements whichever hooks it needs.
//
// The nil guard comes FIRST: composition may hand a nil *ExtensionHost here
// (an application composed without extensions), and operationValidator's own
// guard only runs once the pointer has already been dereferenced by the
// method call.
func (h *ExtensionHost) ImportGate() ImportGate {
	if h == nil {
		return nil
	}
	gate, ok := h.operationValidator().(ImportGate)
	if !ok {
		return nil
	}
	return gate
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

// RootMounts returns the root-routing extensions in registration order, so the
// composition root can register their routes on the application's own router.
// Extensions that do not implement ExtensionRootRouter contribute nothing.
func (h *ExtensionHost) RootMounts() []ExtensionRootMount {
	h.mu.Lock()
	defer h.mu.Unlock()
	mounts := make([]ExtensionRootMount, 0, len(h.extensions))
	for _, ext := range h.extensions {
		if router, ok := ext.(ExtensionRootRouter); ok {
			mounts = append(mounts, ExtensionRootMount{Extension: ext, Router: router})
		}
	}
	return mounts
}

// SelectClass records the class path the deployment selected and tells every
// extension that accepts a selector. It is called during composition, before
// StartExtensions, so an extension can resolve its own settings from the
// selector it was picked by.
func (h *ExtensionHost) SelectClass(class string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.selectedClass = strings.TrimSpace(class)
	extensions := append([]Extension(nil), h.extensions...)
	h.mu.Unlock()
	for _, ext := range extensions {
		if selector, ok := ext.(ExtensionSelector); ok {
			selector.SelectedAs(h.selectedClass)
		}
	}
}

// SelectedClass returns the class path the deployment selected, or "" when it
// selected none.
func (h *ExtensionHost) SelectedClass() string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.selectedClass
}

// StartExtensions runs the startup hook of every registered extension that has
// one, in registration order, stopping at the first failure. The extensions
// whose Start already ran are stopped again before the error is returned, so a
// refused startup does not leave an initialized extension behind.
//
// The returned function runs the shutdown hooks in reverse registration order
// and is idempotent; it stops only the extensions this call started.
func StartExtensions(host *ExtensionHost) (func(), error) {
	if host == nil {
		return func() {}, nil
	}
	host.mu.Lock()
	extensions := append([]Extension(nil), host.extensions...)
	host.mu.Unlock()
	started := make([]ExtensionLifecycle, 0, len(extensions))
	for _, ext := range extensions {
		lifecycle, ok := ext.(ExtensionLifecycle)
		if !ok {
			continue
		}
		if err := lifecycle.Start(); err != nil {
			stopExtensions(started)
			return func() {}, err
		}
		started = append(started, lifecycle)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() { stopExtensions(started) })
	}
	return stop, nil
}

// stopExtensions runs shutdown hooks in reverse registration order, which is
// the order the dependencies were established in.
func stopExtensions(started []ExtensionLifecycle) {
	for i := len(started) - 1; i >= 0; i-- {
		started[i].Stop()
	}
}

// SetSettings records the deployment's extension-only settings on the host. An
// extension reads them through Settings when it is constructed or mounted; a
// host that was given none reports nil.
func (h *ExtensionHost) SetSettings(settings map[string]string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.settings = copySettings(settings)
	h.mu.Unlock()
}

// Settings returns the settings this host holds, or nil when none were set.
func (h *ExtensionHost) Settings() map[string]string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return copySettings(h.settings)
}

// copySettings clones a settings map so callers cannot alias the host's copy.
func copySettings(settings map[string]string) map[string]string {
	if len(settings) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(settings))
	for name, value := range settings {
		cloned[name] = value
	}
	return cloned
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
