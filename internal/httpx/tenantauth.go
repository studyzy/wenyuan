package httpx

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// DetailAuthenticationFailed is the body of a rejected credential, exactly as
// the reference's AuthenticationError renders it.
const DetailAuthenticationFailed = "Authentication failed: Invalid API key"

// DetailAuthenticationUnavailable is the body of a registry failure. It is a
// 503, not a 401: the credential may be valid, but the resolver could not
// answer, and serving the request without a tenant would be worse.
const DetailAuthenticationUnavailable = "Authentication temporarily unavailable"

// The legacy MCP token's refusals, word for word as the reference sends them
// when WENYUAN_API_MCP_AUTH_TOKEN is configured.
const (
	DetailMCPAuthorizationRequired = "Authorization header required"
	DetailMCPInvalidToken          = "Invalid authentication token"
)

// TenantHeader names the tenant an operator credential acts as. It is read on
// every protected request but honored only by a tenant extension that
// recognises the credential as its operator key; a tenant key always resolves
// its own tenant and ignores the header.
const TenantHeader = "X-Wenyuan-Tenant"

// ProtectedPrefixes are the namespaces the reference authenticates: every route
// answered by the memory engine, and the MCP namespace. The monitoring and
// descriptor routes are deliberately not among them, because the reference
// answers those without a credential.
var ProtectedPrefixes = []string{PrefixDefaultV1, NamespaceMCP}

// UnprotectedExactPaths are the routes the reference answers before it reaches
// the engine, so a configured authenticator never sees them.
//
// The retired background route is the only one left: the reference's
// background submission is served ahead of the engine's credential check. The
// retired profile routes are NOT here — reading or writing a profile is an
// engine call, so the reference authenticates it first and only then answers
// the terminal gone status. Leaving them on this list would let an
// unauthenticated caller learn that the route exists and is retired.
var UnprotectedExactPaths = []string{
	PrefixBanksV1 + "/{bank_id}" + SuffixBackground,
}

// TenantAPIKeyMiddleware authenticates every protected request against a
// configured API key, answering 401 with the reference's body when the
// credential is missing or wrong.
//
// A deployment that configures no key is not authenticated at all, which is the
// reference's single-tenant default.
func TenantAPIKeyMiddleware(apiKey string, mcpAuthDisabled bool) Middleware {
	return TenantAuthMiddleware(apiKey, mcpAuthDisabled, "", nil)
}

// TenantAuthMiddleware authenticates protected requests, and the MCP namespace
// against the legacy MCP token when one is configured.
//
// The reference checks the legacy token *instead of* the tenant extension for
// MCP: a deployment that sets it authenticates MCP callers with that token
// alone, which is why the two credentials never apply to the same request.
//
// routeAllowed, when non-nil, reports whether the request is dispatched to a
// route at all. The reference authenticates inside the engine, after routing,
// so a request the router rejects (unmatched path, method mismatch) is answered
// 404 or 405 without a credential; a request the router would reject is
// therefore left unauthenticated for the router's own rejection to stand. The
// MCP namespace is exempt, because the reference authenticates it in a wrapping
// middleware that runs before routing.
func TenantAuthMiddleware(apiKey string, mcpAuthDisabled bool, mcpAuthToken string, routeAllowed func(*http.Request) bool) Middleware {
	if apiKey == "" && mcpAuthToken == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isMCP := pathInNamespace(r.URL.Path, NamespaceMCP)
			if isMCP && mcpAuthToken != "" {
				if !mcpTokenMatches(r, mcpAuthToken) {
					detail := DetailMCPInvalidToken
					if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
						detail = DetailMCPAuthorizationRequired
					}
					// The MCP refusal is the protocol's, so it carries the
					// "error" member rather than the shared "detail" one; the
					// two distinct messages and the status are unchanged.
					WriteJSON(w, http.StatusUnauthorized, MCPErrorEnvelope{Error: detail})
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if !requiresAuthentication(r.URL.Path, mcpAuthDisabled) {
				next.ServeHTTP(w, r)
				return
			}
			// A path that reaches no route is answered by the router, not the
			// engine, so the credential is never examined for it.
			if !isMCP && routeAllowed != nil && !routeAllowed(r) {
				next.ServeHTTP(w, r)
				return
			}
			if !credentialMatches(r, apiKey) {
				WriteJSON(w, http.StatusUnauthorized,
					ErrorEnvelope{Detail: DetailAuthenticationFailed})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// TenantExtensionAuthMiddleware resolves every protected request through the
// tenant extension and places the resolved schema on the request context.
//
// It replaces TenantAuthMiddleware in multi-tenant mode: the extension owns the
// credential check, so the static API key no longer applies. A rejected
// credential is the reference's uniform 401; a resolver failure is a 503 and
// never a fallback to a default schema.
//
// The request is carried to the extension with the operator's tenant header
// already removed, so the extension decides whether one acts as that tenant
// when it recognized the credential as its operator key. No tenant key, and no
// keyless request on a deployment with no operator key, can name a tenant: an
// extension that read the header from the request instead of this field would
// let any caller pick a schema.
func TenantExtensionAuthMiddleware(extension tenant.Extension, mcpAuthDisabled bool, routeAllowed func(*http.Request) bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isMCP := pathInNamespace(r.URL.Path, NamespaceMCP)
			if !requiresAuthentication(r.URL.Path, mcpAuthDisabled) {
				next.ServeHTTP(w, r)
				return
			}
			// A path that reaches no route is answered by the router, not the
			// engine, so the credential is never examined for it.
			if !isMCP && routeAllowed != nil && !routeAllowed(r) {
				next.ServeHTTP(w, r)
				return
			}
			namedTenant := strings.TrimSpace(r.Header.Get(TenantHeader))
			if namedTenant != "" {
				// The header is read here and never down the chain: only the
				// extension's decision about this credential may act on it.
				r.Header.Del(TenantHeader)
			}
			resolved, err := extension.Authenticate(r.Context(), tenant.Request{
				Credential: bearerToken(r),
				TenantID:   namedTenant,
			})
			if err != nil {
				var authErr *tenant.AuthenticationError
				if errors.As(err, &authErr) {
					WriteJSON(w, http.StatusUnauthorized, ErrorEnvelope{Detail: DetailAuthenticationFailed})
					return
				}
				WriteJSON(w, http.StatusServiceUnavailable, ErrorEnvelope{Detail: DetailAuthenticationUnavailable})
				return
			}
			ctx, schemaErr := storage.WithSchema(r.Context(), resolved.Schema)
			if schemaErr != nil {
				WriteJSON(w, http.StatusServiceUnavailable, ErrorEnvelope{Detail: DetailAuthenticationUnavailable})
				return
			}
			// A coding-agent client addresses its bank by the harness name
			// ("coding-agent::<project>"), which is not a tenant id here. The
			// credential has just resolved the tenant, so its id takes the
			// prefix's place before routing captures the bank id.
			rewriteAgentBankPrefix(r.URL, resolved.ID)
			if bankID, foreign := foreignBankID(r, resolved.ID); foreign {
				WriteError(w, NewHTTPError(KindNotFound, BankNotFoundDetail(bankID)))
				return
			}
			r = r.WithContext(ctx)
			if len(resolved.Overrides) > 0 {
				r = WithTenantOverrides(r, resolved.Overrides)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken reads the request's bearer credential, or the bare header when no
// scheme was given, matching the reference's accepted forms.
func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const scheme = "Bearer "
	if len(header) >= len(scheme) && strings.EqualFold(header[:len(scheme)], scheme) {
		return strings.TrimSpace(header[len(scheme):])
	}
	return header
}

// pathInNamespace reports whether a request path is inside a namespace.
func pathInNamespace(path, namespace string) bool {
	return path == namespace || strings.HasPrefix(path, namespace+"/")
}

// mcpTokenMatches compares the request's bearer token with the legacy MCP
// token. The reference reads the token from the Authorization header's bearer
// scheme only, so a bare key is not accepted here.
func mcpTokenMatches(r *http.Request, token string) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const scheme = "Bearer "
	if !strings.HasPrefix(header, scheme) {
		return false
	}
	value := strings.TrimSpace(strings.TrimPrefix(header, scheme))
	return subtle.ConstantTimeCompare([]byte(value), []byte(token)) == 1
}

// foreignBankID reports whether the bank a bank-scoped path addresses belongs to
// some other tenant, and names it.
//
// A bank id is `<tenant>::<name>` and the bank lives in that tenant's schema, so
// a prefix naming another tenant — or no `::` at all, which names no tenant — is
// a bank this caller cannot reach. Letting it through would serve a request
// under the caller's schema while the id claimed another tenant's: the store
// would create or read that prefix as a plain name, and the id would then stop
// meaning what the contract says it means. The refusal is the same 404 an
// absent bank gets, so a caller cannot tell a foreign id from a missing one.
//
// Nothing is foreign for a deployment with no resolved tenant id (the
// single-tenant extension), nor for a path that addresses no bank: the bank
// collection, and every route that is not bank-scoped.
func foreignBankID(r *http.Request, tenantID string) (string, bool) {
	if tenantID == "" {
		return "", false
	}
	bankID, bankScoped := bankScopedPath(r.URL.Path)
	if !bankScoped || bankID == "" {
		return "", false
	}
	prefix, _, ok := strings.Cut(bankID, "::")
	if ok && prefix == tenantID {
		return "", false
	}
	return bankID, true
}

// bankScopedPath splits a bank-scoped path into the bank it addresses.
// @param path - the request's decoded path.
// @returns the first segment after the prefix, and whether the path is
// bank-scoped at all. A path that is not is `false`.
func bankScopedPath(path string) (string, bool) {
	start, ok := agentBankPathStart(path)
	if !ok {
		return "", false
	}
	bankID, _, _ := strings.Cut(path[start:], "/")
	return bankID, true
}

// requiresAuthentication reports whether the path is one the reference
// authenticates.
func requiresAuthentication(path string, mcpAuthDisabled bool) bool {
	for _, exact := range UnprotectedExactPaths {
		if patternMatches(exact, path) {
			return false
		}
	}
	for _, prefix := range ProtectedPrefixes {
		if prefix == NamespaceMCP && mcpAuthDisabled {
			continue
		}
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// patternMatches matches a registered route pattern against a request path,
// treating a brace-delimited segment as one path segment.
func patternMatches(pattern, path string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	if len(patternParts) != len(pathParts) {
		return false
	}
	for i, part := range patternParts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			if pathParts[i] == "" {
				return false
			}
			continue
		}
		if part != pathParts[i] {
			return false
		}
	}
	return true
}

// credentialMatches compares the request's credential with the configured key.
// The credential is either a bearer token or the bare key, the two forms the
// reference accepts.
func credentialMatches(r *http.Request, apiKey string) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const scheme = "Bearer "
	if len(header) >= len(scheme) && strings.EqualFold(header[:len(scheme)], scheme) {
		header = strings.TrimSpace(header[len(scheme):])
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(apiKey)) == 1
}
