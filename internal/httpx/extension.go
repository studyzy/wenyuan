package httpx

import (
	"net/http"
	"sync"
)

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

type muxRegistrar struct{ mux *http.ServeMux }

func (m *muxRegistrar) Handle(method, pattern string, handler http.HandlerFunc) {
	m.mux.HandleFunc(method+" "+pattern, handler)
}
