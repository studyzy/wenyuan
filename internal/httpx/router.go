package httpx

import (
	"context"
	"io"
	"net/http"
	"strings"
)

// RouterOptions configures router construction.
type RouterOptions struct {
	// NullDropFields drops null fields from responses. The reference drops
	// them for every model that can afford it, so this defaults on; a route
	// whose model must keep its nulls opts out per response.
	NullDropFields bool
	// BasePath serves the whole surface under this prefix as well as at the
	// root. Empty means the root only.
	BasePath string
	// TenantAPIKey authenticates every engine-backed request. Empty means no
	// authentication, which is the reference's single-tenant default.
	TenantAPIKey string
	// TenantMCPAuthDisabled exempts the MCP namespace from authentication.
	TenantMCPAuthDisabled bool
	// AccessLog writes one line per request when set. A nil writer leaves the
	// access log off, which is the reference's default.
	AccessLog io.Writer
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
	// ResolveBankAlias maps a bank-scoped path identifier to its canonical bank
	// identifier through the alias table. It is injected by the composition root
	// and applied to every bank-scoped route before the handler runs, so alias
	// resolution happens once at the HTTP edge and nothing below it ever sees an
	// alias. A resolver that returns the input unchanged leaves an unknown name
	// to the handler, which answers not-found (or lazily creates the bank on a
	// write) exactly as it would without one.
	ResolveBankAlias func(ctx context.Context, name string) (string, error)
	// Admission bounds the concurrency of the expensive operations.
	Admission AdmissionConfig
	// Audit, when non-nil, records audited routes to the trail.
	Audit *AuditOptions
}

// Authenticator is the minimal authentication contract for protected routes.
type Authenticator interface {
	Authenticated(r *http.Request) bool
}

// Router owns the root mux and the middleware chain.
type Router struct {
	mux     *http.ServeMux
	options RouterOptions
	// routePatterns records each distinct path template registered, in
	// registration order. The metrics endpoint label is derived from them by
	// matching a request the way the mux would.
	routePatterns []string
	routeSeen     map[string]struct{}
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
	resolveEndpoint := newEndpointResolver(r.routePatterns)
	chain := Chain(
		r.mux,
		BasePathMiddleware(r.options.BasePath),
		TenantAPIKeyMiddleware(r.options.TenantAPIKey, r.options.TenantMCPAuthDisabled),
		AccessLogMiddleware(r.options.AccessLog),
		RouteFallbackMiddleware(),
		DisconnectCancelMiddleware(),
		AdmissionMiddleware(r.options.Admission),
		NullDropMiddleware(r.options.NullDropFields),
		TenantOverridesMiddleware(r.options.Authenticator),
		OperationValidationMiddleware(r.options.Extensions),
		MetricsMiddleware(r.options.Metrics, resolveEndpoint),
		IgnoredParamsMiddleware(),
		GzipMiddleware(r.options.GzipMinSize),
		auditMiddleware(r.options.Audit, resolveEndpoint),
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
//
// A bank-scoped route is wrapped so its {bank_id} path value is rewritten to
// the bank's canonical identifier before the handler runs: an alias resolves to
// the bank it points at, so every surface below the edge — reads, writes,
// configuration and transfer alike — reaches the same bank whichever name the
// caller used. The wrapper runs once the mux has matched, which is the point at
// which the path value exists.
func (r *Router) Handle(method, pattern string, handler http.HandlerFunc) {
	r.recordRoute(pattern)
	r.mux.HandleFunc(method+" "+pattern, r.withBankResolution(pattern, handler))
}

// withBankResolution wraps a handler so a bank-scoped pattern resolves its
// {bank_id} path parameter through the alias table. A pattern without the
// parameter, a router without a resolver, or a name that is already canonical
// is handed to the handler unchanged.
func (r *Router) withBankResolution(pattern string, handler http.HandlerFunc) http.HandlerFunc {
	if !strings.Contains(pattern, "{bank_id}") || r.options.ResolveBankAlias == nil {
		return handler
	}
	return func(w http.ResponseWriter, req *http.Request) {
		raw := req.PathValue("bank_id")
		if raw != "" {
			canonical, err := r.options.ResolveBankAlias(req.Context(), raw)
			if err == nil && canonical != "" && canonical != raw {
				req.SetPathValue("bank_id", canonical)
			}
		}
		handler(w, req)
	}
}

// handleAny registers a handler for a route pattern under every method, which
// the mux spells as a pattern with no method prefix.
func (r *Router) handleAny(pattern string, handler http.Handler) {
	r.recordRoute(pattern)
	r.mux.Handle(pattern, handler)
}

// recordRoute remembers a distinct path template so the metrics endpoint label
// can be resolved later.
func (r *Router) recordRoute(pattern string) {
	if r.routeSeen == nil {
		r.routeSeen = map[string]struct{}{}
	}
	if _, ok := r.routeSeen[pattern]; ok {
		return
	}
	r.routeSeen[pattern] = struct{}{}
	r.routePatterns = append(r.routePatterns, pattern)
}

// newEndpointResolver builds the metrics endpoint-label resolver. It registers
// every path template on a throwaway mux and uses the mux's own matching, so
// the label is the template of the route the request would hit and
// EndpointUnmatched when none matches — the same value the served mux used to
// dispatch.
func newEndpointResolver(patterns []string) func(*http.Request) string {
	mux := http.NewServeMux()
	for _, pattern := range patterns {
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return func(r *http.Request) string {
		if _, pattern := mux.Handler(r); pattern != "" {
			return endpointLabel(pattern)
		}
		return EndpointUnmatched
	}
}

// endpointLabel renders the metrics `endpoint` label for a matched route
// template. Go spells a multi-segment path parameter "{name...}", the same
// converter the reference declares as "{name:path}"; the reference labels by the
// converter-stripped template, so "{chunk_id...}" and "{key...}" must render
// without the wildcard to name the same series the reference emits.
func endpointLabel(pattern string) string {
	if !strings.Contains(pattern, "...}") {
		return pattern
	}
	return strings.ReplaceAll(pattern, "...}", "}")
}

// RegisterArea lets an area contribute its routes through the shared
// namespace contract instead of touching the mux directly.
func (r *Router) RegisterArea(register func(RouteRegistrar)) {
	register(&muxRegistrar{mux: r.mux, record: r.recordRoute})
}

func (r *Router) mountCore() {
	r.mountDescriptorNamespace()
	r.mountExtensionNamespace()
	r.mountMCPNamespace()
}

func (r *Router) mountExtensionNamespace() {
	if r.options.Extensions == nil {
		r.options.Extensions = NewExtensionHost()
	}
	MountExtensions(r.mux, r.options.Extensions, r.extensionChain())
	r.recordRoute(NamespaceExt + "/")
}

func (r *Router) extensionChain() Middleware {
	return func(next http.Handler) http.Handler {
		return Chain(
			next,
			DisconnectCancelMiddleware(),
			NullDropMiddleware(r.options.NullDropFields),
		)
	}
}

func (r *Router) mountMCPNamespace() {
	if !r.options.EnabledMCP {
		return
	}
	if r.options.MCPHandler != nil {
		// The reference serves a single-bank MCP server under /mcp/{bank_id}
		// and leaves the multi-bank one at /mcp; both spellings of the bank
		// path answer, so a client that appends a trailing slash is not sent
		// through a redirect.
		handler := r.extensionChain()(r.options.MCPHandler)
		r.handleAny(NamespaceMCP, handler)
		r.handleAny(NamespaceMCP+"/", handler)
		r.handleAny(NamespaceMCP+"/{bank_id}", handler)
		r.handleAny(NamespaceMCP+"/{bank_id}/", handler)
		return
	}
	mcpMux := http.NewServeMux()
	mcpMux.HandleFunc("POST /", r.mcpUnsupported)
	mcpMux.HandleFunc("GET /", r.mcpUnsupported)
	stripped := http.StripPrefix(NamespaceMCP, mcpMux)
	handler := r.extensionChain()(stripped)
	// The unsupported build answers the same paths the served one does, so a
	// client that probes the single-bank path is told the transport is
	// unavailable rather than being sent through a redirect.
	r.handleAny(NamespaceMCP+"/", handler)
	r.handleAny(NamespaceMCP+"/{bank_id}", handler)
	r.handleAny(NamespaceMCP+"/{bank_id}/", handler)
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
