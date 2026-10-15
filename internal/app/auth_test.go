package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// overrideExtension is a tenant extension whose resolved context carries the
// tenant's configuration overrides.
type overrideExtension struct {
	overrides map[string]any
	last      tenant.Request
}

func (e *overrideExtension) Authenticate(_ context.Context, request tenant.Request) (tenant.Context, error) {
	e.last = request
	return tenant.Context{Schema: "tenant_a", Overrides: e.overrides}, nil
}

func (e *overrideExtension) ListTenants(context.Context) ([]tenant.Tenant, error) {
	return []tenant.Tenant{{Schema: "tenant_a", Overrides: e.overrides}}, nil
}

// The default bearer authenticator does not implement the optional capability,
// which is what guarantees a deployment that keeps it is unaffected.
func TestTokenAuthenticatorLacksTenantOverrideCapability(t *testing.T) {
	var auth httpx.Authenticator = &TokenAuthenticator{Enabled: true}
	if _, ok := auth.(httpx.TenantOverrideSource); ok {
		t.Fatal("the default token authenticator must not supply tenant overrides")
	}
}

// A tenant-aware authenticator supplies the tenant's overrides, so the
// middleware can record them on the request context.
func TestTenantAuthenticatorSuppliesOverrides(t *testing.T) {
	ext := &overrideExtension{overrides: map[string]any{"enable_text_search": false}}
	auth := &TenantAuthenticator{Extension: ext}

	var source httpx.TenantOverrideSource = auth
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", nil)
	req.Header.Set("Authorization", "Bearer secret-token")

	if got := source.TenantOverrides(req); got["enable_text_search"] != false {
		t.Fatalf("overrides = %v, want the tenant's", got)
	}
	if ext.last.Credential != "secret-token" {
		t.Fatalf("credential = %q, want the bearer value without the scheme", ext.last.Credential)
	}
}

// An extension that rejects the request contributes no overrides, so an
// unauthenticated caller never reaches tenant configuration.
func TestTenantAuthenticatorRejectsYieldNoOverrides(t *testing.T) {
	auth := &TenantAuthenticator{Extension: rejectingExtension{}}
	if got := auth.TenantOverrides(httptest.NewRequest(http.MethodPost, "/", nil)); got != nil {
		t.Fatalf("overrides = %v, want none for a rejected request", got)
	}
}

// A nil extension leaves the capability inert rather than panicking.
func TestTenantAuthenticatorWithoutExtensionSuppliesNothing(t *testing.T) {
	auth := &TenantAuthenticator{}
	if got := auth.TenantOverrides(httptest.NewRequest(http.MethodPost, "/", nil)); got != nil {
		t.Fatalf("overrides = %v, want none with no extension", got)
	}
}

// The gate still decides authentication, so the tenant-aware authenticator does
// not loosen or tighten the default gate.
func TestTenantAuthenticatorDelegatesGate(t *testing.T) {
	auth := &TenantAuthenticator{Gate: &TokenAuthenticator{Enabled: true}}
	if auth.Authenticated(httptest.NewRequest(http.MethodPost, "/", nil)) {
		t.Fatal("a request with no credential must not authenticate")
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer token")
	if !auth.Authenticated(req) {
		t.Fatal("a request with a credential must authenticate")
	}
}

type rejectingExtension struct{}

func (rejectingExtension) Authenticate(context.Context, tenant.Request) (tenant.Context, error) {
	return tenant.Context{}, &tenant.AuthenticationError{Reason: "no tenant"}
}

func (rejectingExtension) ListTenants(context.Context) ([]tenant.Tenant, error) {
	return nil, nil
}
