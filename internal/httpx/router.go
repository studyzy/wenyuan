package httpx

import (
	"net/http"
)

// RouterOptions configures router construction.
type RouterOptions struct {
	// NullDropFields enables null-field dropping for every response.
	NullDropFields bool
	// GzipMinSize is the compression floor; negative disables compression.
	GzipMinSize int
	// Metrics receives request counters when non-nil.
	Metrics *MetricsRecorder
	// EnabledBankConfigAPI gates the bank configuration sub-routes.
	EnabledBankConfigAPI bool
	// EnabledMCP gates the optional MCP sub-path.
	EnabledMCP bool
	// MCPHandler answers the MCP namespace when EnabledMCP is set. A nil
	// handler is reported as unsupported rather than mounted, so a build with
	// the gate on but no server wired fails loudly instead of silently.
	MCPHandler http.Handler
	// Extensions supplies deployment-provided routers.
	Extensions *ExtensionHost
	// Authenticator enforces authentication on retired routes. A nil
	// authenticator rejects every unauthenticated request.
	Authenticator Authenticator
	// BankExists reports whether a bank identifier is known. It is injected by
	// the composition root from the storage area.
	BankExists func(bankID string) bool
}

// Authenticator is the minimal authentication contract for protected routes.
type Authenticator interface {
	Authenticated(r *http.Request) bool
}

// Router owns the root mux and the middleware chain.
type Router struct {
	mux     *http.ServeMux
	options RouterOptions
}

// Mux exposes the underlying mux for tests and the application entrypoint.
func (r *Router) Mux() *http.ServeMux { return r.mux }

// NewRouter builds the root router with the core namespaces mounted.
func NewRouter(opts RouterOptions) *Router {
	root := http.NewServeMux()
	r := &Router{mux: root, options: opts}
	r.mountCore()
	return r
}

// Service exposes the fully wrapped handler that answers the public routes.
func (r *Router) Service() http.Handler {
	filter := DefaultQueryFilter()
	chain := Chain(
		r.mux,
		DisconnectCancelMiddleware(),
		NullDropMiddleware(r.options.NullDropFields),
		MetricsMiddleware(r.options.Metrics),
		IgnoredParamsMiddleware(filter),
		GzipMiddleware(r.options.GzipMinSize),
	)
	return chain
}

// NullDropMiddleware records the shaping option for downstream handlers.
func NullDropMiddleware(enabled bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			request := WithEncodeOptions(r, EncodeOptions{DropNullFields: enabled})
			next.ServeHTTP(w, request)
		})
	}
}

// Handle registers a handler on the root mux. It is the public registration
// entry other areas use through the composition root.
func (r *Router) Handle(method, pattern string, handler http.HandlerFunc) {
	r.mux.HandleFunc(method+" "+pattern, handler)
}

// RegisterArea lets an area contribute its routes through the shared
// namespace contract instead of touching the mux directly.
func (r *Router) RegisterArea(register func(RouteRegistrar)) {
	register(&muxRegistrar{mux: r.mux})
}

func (r *Router) mountCore() {
	r.mountExtensionNamespace()
	r.mountMCPNamespace()
}

func (r *Router) mountExtensionNamespace() {
	if r.options.Extensions == nil {
		r.options.Extensions = NewExtensionHost()
	}
	MountExtensions(r.mux, r.options.Extensions, r.extensionChain())
}

func (r *Router) extensionChain() Middleware {
	return func(next http.Handler) http.Handler {
		return Chain(
			next,
			DisconnectCancelMiddleware(),
			NullDropMiddleware(r.options.NullDropFields),
			IgnoredParamsMiddleware(DefaultQueryFilter()),
		)
	}
}

func (r *Router) mountMCPNamespace() {
	if !r.options.EnabledMCP {
		return
	}
	if r.options.MCPHandler != nil {
		r.mux.Handle(NamespaceMCP, r.extensionChain()(r.options.MCPHandler))
		r.mux.Handle(NamespaceMCP+"/", r.extensionChain()(r.options.MCPHandler))
		return
	}
	mcpMux := http.NewServeMux()
	mcpMux.HandleFunc("POST /", r.mcpUnsupported)
	mcpMux.HandleFunc("GET /", r.mcpUnsupported)
	stripped := http.StripPrefix(NamespaceMCP, mcpMux)
	r.mux.Handle(NamespaceMCP+"/", r.extensionChain()(stripped))
}

func (r *Router) mcpUnsupported(w http.ResponseWriter, req *http.Request) {
	WriteErrorFor(w, req, NewHTTPError(KindUnprocessable, "MCP transport is not implemented in this build"))
}

// BankPath returns the bank-scoped route pattern for a suffix, keyed by the
// versioned namespace and the {bank_id} path parameter.
func BankPath(suffix string) string {
	return PrefixBanksV1 + "/{bank_id}" + suffix
}

// BanksCollectionPath returns the bank collection route pattern.
func BanksCollectionPath() string { return PrefixBanksV1 }

// BankMemberPath returns the bank member route pattern.
func BankMemberPath() string { return PrefixBanksV1 + "/{bank_id}" }
