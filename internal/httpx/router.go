package httpx

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RouterOptions configures router construction.
type RouterOptions struct {
	// BasePath serves the whole surface under this prefix as well as at the
	// root. Empty means the root only.
	BasePath string
	// TenantAPIKey authenticates every engine-backed request. Empty means no
	// authentication, which is the reference's single-tenant default.
	TenantAPIKey string
	// TenantMCPAuthDisabled exempts the MCP namespace from authentication.
	TenantMCPAuthDisabled bool
	// MCPAuthToken is the legacy MCP credential. When set, the MCP namespace is
	// authenticated with it instead of the tenant key, as the reference does.
	MCPAuthToken string
	// AccessLog writes one line per request when set. A nil writer leaves the
	// access log off, which is the reference's default.
	AccessLog io.Writer
	// TrustedProxies makes the forwarded client headers believed, but only from
	// these peers. It is empty unless the operator asked for proxy headers, and
	// an empty list believes no caller.
	TrustedProxies []string
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
	// methods records, per registered path template, the methods an API route
	// declared. The reference's method set is exactly the decorator's, so a GET
	// route does not admit HEAD; the router renders the reference's Allow header
	// and rejects HEAD from this set rather than from the mux's own, which adds
	// HEAD to every GET.
	methods map[string]map[string]struct{}
	// methodOrder records, per path template, the methods in the order they
	// were declared. The reference's router answers a method mismatch with the
	// Allow header of the first route that partially matched — the first route
	// declared on that path — so a path declared for both POST and DELETE
	// reports `Allow: POST`, not both.
	methodOrder map[string][]string
	// descriptor marks the routes FastAPI adds for its own documentation
	// surface. They are plain Starlette routes, which do add HEAD to a GET, so
	// they admit HEAD and report `Allow: HEAD, GET` — unlike an API route.
	descriptor map[string]struct{}
	// probe matches a request path to its path template for the auth/routing
	// order and for the Allow header.
	probe *http.ServeMux
	// finalizeOnce guards the one-time registration of the method-rejection
	// handlers, which need the complete method set and so cannot run as routes
	// are added.
	finalizeOnce bool
}

// recordMethod notes that an API route declared a method on a path template.
// The descriptor flag marks a Starlette-style route, which admits HEAD.
func (r *Router) recordMethod(pattern, method string, descriptor bool) {
	if r.methods == nil {
		r.methods = map[string]map[string]struct{}{}
	}
	if r.methods[pattern] == nil {
		r.methods[pattern] = map[string]struct{}{}
	}
	if _, seen := r.methods[pattern][method]; !seen {
		r.methods[pattern][method] = struct{}{}
		if r.methodOrder == nil {
			r.methodOrder = map[string][]string{}
		}
		r.methodOrder[pattern] = append(r.methodOrder[pattern], method)
	}
	if descriptor {
		if r.descriptor == nil {
			r.descriptor = map[string]struct{}{}
		}
		r.descriptor[pattern] = struct{}{}
	}
}

// allowHeader renders the value of the Allow header for a path template, as the
// reference's router does. The reference's router answers a method mismatch
// with the Allow header of the first route that partially matched — the first
// route declared on the path — so a path declared for POST and then DELETE
// reports only `POST`. A descriptor route is the exception: its GET implies
// HEAD, and the reference renders the augmented set with HEAD first.
func (r *Router) allowHeader(pattern string) string {
	order := r.methodOrder[pattern]
	if len(order) == 0 {
		return ""
	}
	if _, ok := r.descriptor[pattern]; ok {
		return http.MethodHead + ", " + order[0]
	}
	return order[0]
}

// finalize prepares the routing metadata the auth/routing order and the method
// semantics need. It runs once, from Service, because it must see the complete
// method set.
func (r *Router) finalize() {
	if r.finalizeOnce {
		return
	}
	r.finalizeOnce = true
	r.probe = newProbeMux(r.routePatterns)
}

// newProbeMux builds a mux over the registered path templates so a request path
// can be resolved to its template without dispatching a handler.
func newProbeMux(patterns []string) *http.ServeMux {
	mux := http.NewServeMux()
	for _, pattern := range patterns {
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return mux
}

// probePattern resolves a request path to its registered path template, or ""
// when no route matches.
func (r *Router) probePattern(req *http.Request) string {
	if r.probe == nil {
		return ""
	}
	if _, pattern := r.probe.Handler(req); pattern != "" {
		return pattern
	}
	return ""
}

// undeclaredMCPPath answers a path deeper than either MCP mount the reference
// declares, before the mux can match it against the bank mount's trailing slash.
// The reference's /mcp and /mcp/{bank_id} are whole paths, so /mcp/a/b matches
// nothing and its 404 is plain text; a Go mux would instead let /mcp/{bank_id}/
// swallow the deeper path and dispatch it to the MCP handler.
func (r *Router) undeclaredMCPPath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if r.undeclaredMCPDepth(req) {
			// The reference's Router answers an unmatched path itself, as plain
			// text, and never reaches the JSON error handler; the content type
			// says text so the fallback passes this through rather than
			// re-rendering it into the API's envelope.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Not Found"))
			return
		}
		next.ServeHTTP(w, req)
	})
}

// undeclaredMCPDepth reports whether a request matched an MCP mount only because
// the mux let a pattern carry segments the reference does not register. The
// reference's /mcp/{bank_id} is a whole path, so /mcp/a/b matches no route and
// its 404 is plain text; the MCP server itself serves /mcp/{bank}/tools/list.
func (r *Router) undeclaredMCPDepth(req *http.Request) bool {
	path := req.URL.Path
	if r.options.BasePath != "" {
		path = strings.TrimPrefix(path, r.options.BasePath)
	}
	if !strings.HasPrefix(path, NamespaceMCP+"/") {
		return false
	}
	// namespace, bank — then anything further is deeper than either mount the
	// reference declares, whatever the extra segments spell. The reference's
	// /mcp/{bank_id} is a whole path, so /mcp/a/tools/list is a 404 too.
	segments := strings.Split(strings.Trim(path, "/"), "/")
	return len(segments) > 2 && segments[1] != "" && segments[0] == strings.TrimPrefix(NamespaceMCP, "/")
}

// declared reports whether the router recorded the methods of a path template.
// An extension route registers directly on the mux, so its methods are unknown
// here and its method semantics are left to the mux.
func (r *Router) declared(pattern string) map[string]struct{} {
	return r.methods[pattern]
}

// allows reports whether a path template accepts a method. A descriptor route's
// GET implies HEAD; an API route accepts exactly the methods it declared.
func (r *Router) allows(pattern, method string) bool {
	if _, ok := r.descriptor[pattern]; ok {
		return method == http.MethodGet || method == http.MethodHead
	}
	_, ok := r.methods[pattern][method]
	return ok
}

// rawPathMiddleware stops the standard mux from canonicalising a request path
// before it dispatches.
//
// A mux cleans the path it matches on and answers an unclean path — a doubled
// slash or a "." or ".." element — with a 301 to the cleaned spelling before
// any handler runs. The reference's router does not: it routes the path as
// written, so a doubled slash after a namespace is part of the captured value
// and a path that matches no route is a 404, never a redirect. The middleware
// rewrites only the escaped path the mux matches on, to a form of the caller's
// path that the mux already considers clean; r.URL.Path is left untouched, so
// a handler still reads the caller's path and wildcard captures are unchanged.
func rawPathMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			escaped := req.URL.EscapedPath()
			if hasEscapedSlash(escaped) {
				// The reference's router matches on the decoded path, so an
				// escaped slash becomes a real separator and the path matches no
				// route (a single-segment parameter cannot contain one). Go's mux
				// matches on segments instead, so "%2F" would stay inside the
				// captured value and reach the handler: a knowledge page id of
				// "a/b" answered "not found" here where the reference answers the
				// framework's 404. Letting the mux split it restores that.
				req.URL.Path = unescapeEscapedSlashes(req.URL.Path)
				req.URL.RawPath = ""
			} else if raw := unrestrainedRawPath(escaped); raw != escaped {
				req.URL.RawPath = raw
			}
			next.ServeHTTP(w, req)
		})
	}
}

// hasEscapedSlash reports whether the escaped path carries a %2F or %2f, the one
// escape that changes how many segments the path has.
func hasEscapedSlash(escapedPath string) bool {
	return strings.Contains(escapedPath, "%2F") || strings.Contains(escapedPath, "%2f")
}

// unescapeEscapedSlashes turns "%2F"/"%2f" into "/" in an already-decoded path
// that lost the information through double-encoding, leaving every other
// character alone.
func unescapeEscapedSlashes(decodedPath string) string {
	decodedPath = strings.ReplaceAll(decodedPath, "%2F", "/")
	return strings.ReplaceAll(decodedPath, "%2f", "/")
}

// unrestrainedRawPath escapes the elements the mux's path cleaning would
// collapse, so it matches the path as written. An empty element (a doubled
// slash) survives because the separator that follows it is escaped, turning
// "//" into "/%2F"; a "." or ".." element survives because a dot is escaped.
// Every escape unescapes back to the character it replaces, and a path with
// nothing to escape is returned unchanged.
func unrestrainedRawPath(escapedPath string) string {
	segments := strings.Split(escapedPath, "/")
	var b strings.Builder
	b.Grow(len(escapedPath) + 8)
	for i, segment := range segments {
		if i > 0 {
			if segments[i-1] == "" && i-1 != 0 {
				// The preceding element is empty and interior, so cleanPath
				// would drop it. Escape this separator to keep it a real
				// element without changing what the path unescapes to.
				b.WriteString("%2F")
			} else {
				b.WriteByte('/')
			}
		}
		switch segment {
		case ".":
			b.WriteString("%2E")
		case "..":
			b.WriteString("%2E%2E")
		default:
			b.WriteString(segment)
		}
	}
	return b.String()
}

// methodSemanticsMiddleware applies the reference's method semantics to the
// routes the router knows: a HEAD request to an API route that declared only
// GET is rejected 405, and the Allow header of a method rejection reports the
// declared methods rather than the mux's own alphabetically sorted set (which
// silently adds HEAD to every GET). A descriptor route keeps its implied HEAD.
// A pattern whose methods the router does not know is passed through untouched.
func (r *Router) methodSemantics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		pattern := r.probePattern(req)
		spurious := uncleanMatch(pattern, req.URL.Path)
		if spurious {
			// The match came only from a doubled slash the mux merged away, so
			// the reference would match no route: fall through to its slash
			// redirect or its 404 rather than dispatching to the handler.
			pattern = ""
		}
		if pattern == "" {
			// No route is dispatched to, so whatever answers the request (the
			// slash redirect below, or the fallback's 404) is not the reporting
			// route class and carries no ignored-parameter header, exactly as
			// the reference's unmatched path does not.
			SuppressIgnoredParams(req)
		}
		if pattern == "" && req.URL.Path != "/" && strings.HasSuffix(req.URL.Path, "/") {
			// The reference's router retries the slash-stripped path against
			// the routes and, when it matches, redirects to it — whatever the
			// method, and with the query preserved. It strips every trailing
			// slash, not just one, so `/banks//` redirects to `/banks`. A path
			// that is itself a registered route (the MCP spellings) is served,
			// not redirected.
			if stripped := strings.TrimRight(req.URL.Path, "/"); stripped != "" && r.probePatternPath(stripped) != "" {
				location := r.options.BasePath + stripped
				if req.URL.RawQuery != "" {
					location += "?" + req.URL.RawQuery
				}
				redirect(w, req, location)
				return
			}
		}
		if spurious {
			// No route matches the path as the reference reads it, and the
			// slash redirect did not apply: answer the reference's 404. The
			// escaped path the mux would match on is left alone; this reply
			// reflects the router's own rejection, which the fallback
			// middleware renders in the reference's JSON envelope.
			http.NotFound(w, req)
			return
		}
		if r.declared(pattern) == nil {
			next.ServeHTTP(w, req)
			return
		}
		if !r.allows(pattern, req.Method) {
			// A method mismatch is answered by the router's own 405, before any
			// route handler runs, so the reference emits no ignored-parameter
			// header for it.
			SuppressIgnoredParams(req)
		}
		if req.Method == http.MethodHead && !r.allows(pattern, http.MethodHead) {
			w.Header().Set("Allow", r.allowHeader(pattern))
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(&allowWriter{ResponseWriter: w, router: r, pattern: pattern}, req)
	})
}

// probePatternPath resolves a path (with no request method) to its registered
// path template.
func (r *Router) probePatternPath(path string) string {
	if r.probe == nil {
		return ""
	}
	if _, pattern := r.probe.Handler(&http.Request{Method: http.MethodGet, URL: &url.URL{Path: path}}); pattern != "" && !uncleanMatch(pattern, path) {
		return pattern
	}
	return ""
}

// uncleanMatch reports whether the mux matched a request only because its
// escaped-path matching merged an empty element — a doubled slash — into the
// following segment. A single-segment wildcard can then capture a value the
// reference's router would never route to it, because the reference matches no
// route when a doubled slash leaves an empty element that the pattern's literal
// segments and single-segment wildcards cannot absorb. A tail wildcard
// ("{name...}") absorbs the whole remainder and keeps matching its literal
// prefix, so a doubled slash after the prefix is part of the captured value
// there and the match is genuine.
func uncleanMatch(pattern, path string) bool {
	return pattern != "" && strings.Contains(path, "//") && !strings.Contains(pattern, "...}")
}

// allowWriter corrects the Allow header on a method rejection the mux produced,
// so it reports the reference's declared method set.
type allowWriter struct {
	http.ResponseWriter
	router  *Router
	pattern string
	wrote   bool
}

func (w *allowWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	if status == http.StatusMethodNotAllowed {
		if allow := w.router.allowHeader(w.pattern); allow != "" {
			w.Header().Set("Allow", allow)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *allowWriter) Write(body []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// routeAllowed reports whether the request is dispatched to a handler, which is
// what the reference's authentication follows: it answers an unmatched path
// with 404 and a method mismatch with 405 before any credential is examined.
// A request that would not reach a handler is therefore left unauthenticated so
// the router's own rejection stands. The MCP namespace is exempt, because the
// reference authenticates it in a wrapping middleware before routing.
func (r *Router) routeAllowed(req *http.Request) bool {
	pattern := r.probePattern(req)
	if pattern == "" || uncleanMatch(pattern, req.URL.Path) {
		return false
	}
	return r.allows(pattern, req.Method)
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
	r.finalize()
	resolveEndpoint := newEndpointResolver(r.routePatterns)
	chain := Chain(
		r.undeclaredMCPPath(r.mux),
		ProxyHeadersMiddleware(r.options.TrustedProxies),
		BasePathMiddleware(r.options.BasePath),
		rawPathMiddleware(),
		TenantAuthMiddleware(r.options.TenantAPIKey, r.options.TenantMCPAuthDisabled, r.options.MCPAuthToken, r.routeAllowed),
		AccessLogMiddleware(r.options.AccessLog),
		RecoveryMiddleware(),
		RouteFallbackMiddleware(),
		DisconnectCancelMiddleware(),
		AdmissionMiddleware(r.options.Admission),
		TenantOverridesMiddleware(r.options.Authenticator),
		OperationValidationMiddleware(r.options.Extensions),
		MetricsMiddleware(r.options.Metrics, resolveEndpoint),
		IgnoredParamsMiddleware(),
		GzipMiddleware(r.options.GzipMinSize),
		auditMiddleware(r.options.Audit, resolveEndpoint),
		r.methodSemantics,
	)
	return chain
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
	r.recordMethod(pattern, method, false)
	r.mux.HandleFunc(method+" "+pattern, r.withBankResolution(pattern, handler))
}

// handleDescriptor registers a Starlette-style documentation route: its GET
// implies HEAD, so HEAD is served rather than rejected, and its Allow header
// reports `HEAD, GET`. It is how the reference's FastAPI adds /openapi.json,
// /docs, /docs/oauth2-redirect and /redoc.
func (r *Router) handleDescriptor(pattern string, handler http.HandlerFunc) {
	r.recordRoute(pattern)
	r.recordMethod(pattern, http.MethodGet, true)
	r.mux.HandleFunc(http.MethodGet+" "+pattern, handler)
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
