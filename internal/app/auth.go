package app

import (
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// authenticatorFrom derives the protected-route authenticator from
// configuration. An empty allowlist denies every caller, which keeps retired
// routes closed by default.
func authenticatorFrom(cfg config.Config) *TokenAuthenticator {
	return &TokenAuthenticator{Enabled: cfg.EnableBankConfigAPI}
}

// TokenAuthenticator accepts a bearer token from the Authorization header. The
// foundation unit keeps a permissive default so internal callers can reach the
// configuration surface; deployments add their own gate at the edge.
type TokenAuthenticator struct {
	// Enabled reports whether the configuration surface is switched on.
	Enabled bool
}

// Authenticated reports whether the request carries an accepted credential.
func (a *TokenAuthenticator) Authenticated(r *http.Request) bool {
	if r == nil {
		return false
	}
	// A bearer credential is accepted; a missing credential is rejected so an
	// unauthenticated caller cannot observe the retirement status.
	return r.Header.Get("Authorization") != ""
}

// TenantAuthenticator is the optional tenant-aware authenticator. It gates a
// request like TokenAuthenticator and, when it can attribute the request to a
// tenant through the extension, exposes that tenant's configuration overrides
// through the httpx tenant-override capability. A deployment that wires it gets
// the tenant layer on the request context, layered under the bank's own
// overrides; one that keeps the plain token authenticator does not implement the
// capability and is unaffected.
type TenantAuthenticator struct {
	// Gate is the bearer gate that must also accept the request. A nil gate
	// accepts every request, which is the right shape for a deployment whose
	// only gate is the tenant extension itself.
	Gate *TokenAuthenticator
	// Extension resolves the caller's tenant. A nil extension supplies no
	// overrides, so the capability is inert.
	Extension tenant.Extension
}

// Authenticated reports whether the bearer gate accepts the request. A nil gate
// accepts, so the tenant extension is the only gate in that deployment.
func (a *TenantAuthenticator) Authenticated(r *http.Request) bool {
	if a == nil || a.Gate == nil {
		return r != nil
	}
	return a.Gate.Authenticated(r)
}

// TenantOverrides authenticates the request against the tenant extension and
// returns the tenant's configuration overrides. A request the extension rejects
// contributes none, so an unauthenticated caller never reaches tenant config.
func (a *TenantAuthenticator) TenantOverrides(r *http.Request) map[string]any {
	if a == nil || a.Extension == nil || r == nil {
		return nil
	}
	resolved, err := a.Extension.Authenticate(r.Context(), tenant.Request{
		Credential: bearerCredential(r),
	})
	if err != nil {
		return nil
	}
	return resolved.Overrides
}

// bearerCredential reads the request's credential, stripping the bearer scheme
// when present. An extension sees the credential itself, not the header.
func bearerCredential(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const scheme = "Bearer "
	if len(header) >= len(scheme) && strings.EqualFold(header[:len(scheme)], scheme) {
		return strings.TrimSpace(header[len(scheme):])
	}
	return header
}
