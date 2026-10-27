package httpx

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// DetailAuthenticationFailed is the body of a rejected credential, exactly as
// the reference's AuthenticationError renders it.
const DetailAuthenticationFailed = "Authentication failed: Invalid API key"

// The legacy MCP token's refusals, word for word as the reference sends them
// when WENYUAN_API_MCP_AUTH_TOKEN is configured.
const (
	DetailMCPAuthorizationRequired = "Authorization header required"
	DetailMCPInvalidToken          = "Invalid authentication token"
)

// ProtectedPrefixes are the namespaces the reference authenticates: every route
// answered by the memory engine, and the MCP namespace. The monitoring and
// descriptor routes are deliberately not among them, because the reference
// answers those without a credential.
var ProtectedPrefixes = []string{PrefixDefaultV1, NamespaceMCP}

// UnprotectedExactPaths are the routes the reference answers before it reaches
// the engine, so a configured authenticator never sees them.
var UnprotectedExactPaths = []string{
	PrefixBanksV1 + "/{bank_id}" + SuffixProfile,
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
					WriteJSON(w, http.StatusUnauthorized,
						ErrorEnvelope{Detail: detail})
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
