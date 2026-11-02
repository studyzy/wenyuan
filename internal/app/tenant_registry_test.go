package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

func seedActiveTenant(t *testing.T, registry *storage.MemoryTenantRegistry, id string, config map[string]any) (string, tenant.Key) {
	t.Helper()
	now := time.Now().UTC()
	if err := registry.CreateTenant(context.Background(), tenant.Record{
		ID: id, DisplayName: id, Status: tenant.StatusActive, Config: config, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create tenant %s: %v", id, err)
	}
	plaintext, key, err := tenant.NewKey(now)
	if err != nil {
		t.Fatalf("new key: %v", err)
	}
	key.TenantID = id
	if err := registry.CreateKey(context.Background(), key); err != nil {
		t.Fatalf("create key: %v", err)
	}
	return plaintext, key
}

func TestRegistryExtensionAuthenticate(t *testing.T) {
	ctx := context.Background()
	registry := storage.NewMemoryTenantRegistry()
	plaintext, key := seedActiveTenant(t, registry, "acme", map[string]any{"enable_text_search": false})
	extension := NewRegistryExtension(registry, "")

	resolved, err := extension.Authenticate(ctx, tenant.Request{Credential: plaintext})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if resolved.ID != "acme" || resolved.Schema != "acme" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved.Overrides["enable_text_search"] != false {
		t.Fatalf("overrides = %+v", resolved.Overrides)
	}

	for _, tc := range []struct {
		name       string
		credential string
	}{
		{name: "missing", credential: ""},
		{name: "unknown", credential: "wyk_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := extension.Authenticate(ctx, tenant.Request{Credential: tc.credential}); !errors.Is(err, tenant.ErrAuthentication) {
				t.Fatalf("Authenticate(%q) = %v, want ErrAuthentication", tc.credential, err)
			}
		})
	}

	now := time.Now().UTC()
	expiredAt := now.Add(-time.Minute)
	expiredPlain, expiredKey, err := tenant.NewKey(now)
	if err != nil {
		t.Fatalf("new expired key: %v", err)
	}
	expiredKey.TenantID = "acme"
	expiredKey.ExpiresAt = &expiredAt
	if err := registry.CreateKey(ctx, expiredKey); err != nil {
		t.Fatalf("create expired key: %v", err)
	}
	if _, err := extension.Authenticate(ctx, tenant.Request{Credential: expiredPlain}); !errors.Is(err, tenant.ErrAuthentication) {
		t.Fatalf("expired key = %v, want ErrAuthentication", err)
	}

	revokedAt := now
	if err := registry.RevokeKey(ctx, "acme", key.ID, revokedAt); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := extension.Authenticate(ctx, tenant.Request{Credential: plaintext}); !errors.Is(err, tenant.ErrAuthentication) {
		t.Fatalf("revoked key = %v, want ErrAuthentication", err)
	}
}

func TestRegistryExtensionRejectsInactiveTenants(t *testing.T) {
	ctx := context.Background()
	registry := storage.NewMemoryTenantRegistry()
	plaintext, _ := seedActiveTenant(t, registry, "acme", nil)
	extension := NewRegistryExtension(registry, "")

	suspended := tenant.StatusSuspended
	if _, err := registry.UpdateTenant(ctx, "acme", storage.TenantUpdate{Status: &suspended}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := extension.Authenticate(ctx, tenant.Request{Credential: plaintext}); !errors.Is(err, tenant.ErrAuthentication) {
		t.Fatalf("suspended tenant = %v, want ErrAuthentication", err)
	}

	active := tenant.StatusActive
	if _, err := registry.UpdateTenant(ctx, "acme", storage.TenantUpdate{Status: &active}); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if _, err := registry.DeleteTenant(ctx, "acme", time.Now().UTC()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := extension.Authenticate(ctx, tenant.Request{Credential: plaintext}); !errors.Is(err, tenant.ErrAuthentication) {
		t.Fatalf("deleted tenant = %v, want ErrAuthentication", err)
	}
}

func TestRegistryExtensionOperatorResolvesNamedTenant(t *testing.T) {
	ctx := context.Background()
	registry := storage.NewMemoryTenantRegistry()
	tenantKey, _ := seedActiveTenant(t, registry, "acme", map[string]any{"enable_text_search": false})
	seedActiveTenant(t, registry, "beta", nil)
	extension := NewRegistryExtension(registry, "operator-secret")

	resolved, err := extension.Authenticate(ctx, tenant.Request{Credential: "operator-secret", TenantID: "beta"})
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if resolved.ID != "beta" || resolved.Schema != "beta" {
		t.Fatalf("resolved = %+v, want beta", resolved)
	}

	fromOperator, err := extension.Authenticate(ctx, tenant.Request{Credential: "operator-secret", TenantID: "acme"})
	if err != nil {
		t.Fatalf("Authenticate acme: %v", err)
	}
	if fromOperator.Overrides["enable_text_search"] != false {
		t.Fatalf("overrides = %+v", fromOperator.Overrides)
	}

	// A tenant key resolves its own tenant even when the header names another
	// one: the operator branch is the only caller of the named tenant.
	own, err := extension.Authenticate(ctx, tenant.Request{Credential: tenantKey, TenantID: "beta"})
	if err != nil {
		t.Fatalf("Authenticate tenant key: %v", err)
	}
	if own.ID != "acme" {
		t.Fatalf("resolved = %+v, want acme", own)
	}

	for _, tc := range []struct {
		name       string
		credential string
		tenantID   string
	}{
		{name: "operator without a tenant", credential: "operator-secret"},
		{name: "operator naming an unknown tenant", credential: "operator-secret", tenantID: "missing"},
		{name: "non-operator naming a tenant", credential: "wyk_unknown", tenantID: "beta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := extension.Authenticate(ctx, tenant.Request{Credential: tc.credential, TenantID: tc.tenantID})
			if !errors.Is(err, tenant.ErrAuthentication) {
				t.Fatalf("Authenticate(%q, %q) = %v, want ErrAuthentication", tc.credential, tc.tenantID, err)
			}
		})
	}

	suspended := tenant.StatusSuspended
	if _, err := registry.UpdateTenant(ctx, "beta", storage.TenantUpdate{Status: &suspended}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := extension.Authenticate(ctx, tenant.Request{Credential: "operator-secret", TenantID: "beta"}); !errors.Is(err, tenant.ErrAuthentication) {
		t.Fatalf("suspended tenant = %v, want ErrAuthentication", err)
	}
}

func TestRegistryExtensionWithoutOperatorKeyIgnoresTenantHeader(t *testing.T) {
	ctx := context.Background()
	registry := storage.NewMemoryTenantRegistry()
	seedActiveTenant(t, registry, "acme", nil)
	extension := NewRegistryExtension(registry, "")

	_, err := extension.Authenticate(ctx, tenant.Request{Credential: "operator-secret", TenantID: "acme"})
	if !errors.Is(err, tenant.ErrAuthentication) {
		t.Fatalf("Authenticate = %v, want ErrAuthentication", err)
	}
}

func TestRegistryExtensionListTenants(t *testing.T) {
	ctx := context.Background()
	registry := storage.NewMemoryTenantRegistry()
	seedActiveTenant(t, registry, "acme", map[string]any{"retain_chunk_size": 200})
	seedActiveTenant(t, registry, "beta", nil)
	now := time.Now().UTC()
	if err := registry.CreateTenant(ctx, tenant.Record{
		ID: "gamma", DisplayName: "gamma", Status: tenant.StatusSuspended, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create gamma: %v", err)
	}

	extension := NewRegistryExtension(registry, "")
	tenants, err := extension.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("tenants = %+v, want acme and beta", tenants)
	}
	if tenants[0].ID != "acme" || tenants[0].Schema != "acme" {
		t.Fatalf("first tenant = %+v", tenants[0])
	}
	if tenants[0].Overrides["retain_chunk_size"] != 200 {
		t.Fatalf("overrides = %+v", tenants[0].Overrides)
	}
}
