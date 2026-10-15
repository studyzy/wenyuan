package httpx

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// DetailAuthenticationFailed is the body of a rejected credential, exactly as
// the reference's AuthenticationError renders it.
const DetailAuthenticationFailed = "Authentication failed: Invalid API key"

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
	if apiKey == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !requiresAuthentication(r.URL.Path, mcpAuthDisabled) {
				next.ServeHTTP(w, r)
				return
			}
			if !credentialMatches(r, apiKey) {
				WriteJSON(w, http.StatusUnauthorized,
					ErrorEnvelope{Detail: DetailAuthenticationFailed}, EncodeOptionsFor(r))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
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
