package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

func TestMemoryTenantRegistryTenantLifecycle(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryTenantRegistry()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	record := tenant.Record{
		ID:          "acme",
		DisplayName: "Acme",
		Status:      tenant.StatusProvisioning,
		Config:      map[string]any{"enable_text_search": false},
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := registry.CreateTenant(ctx, record); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := registry.CreateTenant(ctx, record); !errors.Is(err, tenant.ErrExists) {
		t.Fatalf("duplicate CreateTenant = %v, want ErrExists", err)
	}

	got, err := registry.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	got.Config["enable_text_search"] = true
	again, err := registry.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("GetTenant after mutation: %v", err)
	}
	if again.Config["enable_text_search"] != false {
		t.Fatal("GetTenant returned a Config map shared with the registry")
	}

	name := "Acme Inc"
	active := tenant.StatusActive
	updated, err := registry.UpdateTenant(ctx, "acme", TenantUpdate{DisplayName: &name, Status: &active})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if updated.DisplayName != name || updated.Status != tenant.StatusActive {
		t.Fatalf("updated = %+v", updated)
	}

	provisioning := tenant.StatusProvisioning
	if _, err := registry.UpdateTenant(ctx, "acme", TenantUpdate{Status: &provisioning}); !errors.Is(err, tenant.ErrInvalidTransition) {
		t.Fatalf("active -> provisioning = %v, want ErrInvalidTransition", err)
	}

	deleted, err := registry.DeleteTenant(ctx, "acme", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if deleted.Status != tenant.StatusDeleted || deleted.DeletedAt == nil {
		t.Fatalf("deleted = %+v", deleted)
	}
	if _, err := registry.DeleteTenant(ctx, "acme", now.Add(2*time.Hour)); err != nil {
		t.Fatalf("second DeleteTenant should be idempotent: %v", err)
	}

	if _, err := registry.GetTenant(ctx, "missing"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("GetTenant(missing) = %v, want ErrNotFound", err)
	}
}

func TestMemoryTenantRegistryListTenants(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryTenantRegistry()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	for _, id := range []string{"beta", "alpha", "gamma"} {
		status := tenant.StatusActive
		if id == "gamma" {
			status = tenant.StatusSuspended
		}
		if err := registry.CreateTenant(ctx, tenant.Record{
			ID: id, DisplayName: id, Status: status, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}

	active, err := registry.ListTenants(ctx, TenantFilter{Status: tenant.StatusActive})
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(active) != 2 || active[0].ID != "alpha" || active[1].ID != "beta" {
		t.Fatalf("active tenants = %+v, want alpha,beta", active)
	}

	page, err := registry.ListTenants(ctx, TenantFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("ListTenants page: %v", err)
	}
	if len(page) != 1 || page[0].ID != "beta" {
		t.Fatalf("page = %+v, want beta", page)
	}

	matched, err := registry.ListTenants(ctx, TenantFilter{Query: "AMM"})
	if err != nil {
		t.Fatalf("ListTenants query: %v", err)
	}
	if len(matched) != 1 || matched[0].ID != "gamma" {
		t.Fatalf("query = %+v, want gamma", matched)
	}
}

func TestMemoryTenantRegistryKeys(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryTenantRegistry()
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

	if err := registry.CreateTenant(ctx, tenant.Record{
		ID: "acme", DisplayName: "Acme", Status: tenant.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, key, err := tenant.NewKey(now)
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	key.TenantID = "acme"
	if err := registry.CreateKey(ctx, key); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if err := registry.CreateKey(ctx, key); !errors.Is(err, tenant.ErrExists) {
		t.Fatalf("duplicate CreateKey = %v, want ErrExists", err)
	}

	gotKey, gotTenant, err := registry.LookupKey(ctx, key.Hash)
	if err != nil {
		t.Fatalf("LookupKey: %v", err)
	}
	if gotKey.ID != key.ID || gotTenant.ID != "acme" {
		t.Fatalf("lookup = %+v / %+v", gotKey, gotTenant)
	}
	gotKey.Hash[0] ^= 0xff
	again, _, err := registry.LookupKey(ctx, key.Hash)
	if err != nil {
		t.Fatalf("LookupKey after mutation: %v", err)
	}
	if again.Hash[0] != key.Hash[0] {
		t.Fatal("LookupKey returned a hash slice shared with the registry")
	}

	wrong := tenant.HashKey("wyk_wrong")
	if _, _, err := registry.LookupKey(ctx, wrong); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("LookupKey(wrong) = %v, want ErrNotFound", err)
	}

	if err := registry.TouchKey(ctx, key.ID, now.Add(time.Minute)); err != nil {
		t.Fatalf("TouchKey: %v", err)
	}
	touched, _, err := registry.LookupKey(ctx, key.Hash)
	if err != nil {
		t.Fatalf("LookupKey after touch: %v", err)
	}
	if touched.LastUsedAt == nil || !touched.LastUsedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("last_used_at = %v", touched.LastUsedAt)
	}

	if err := registry.RevokeKey(ctx, "acme", key.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}
	revoked, _, err := registry.LookupKey(ctx, key.Hash)
	if err != nil {
		t.Fatalf("LookupKey after revoke: %v", err)
	}
	if revoked.RevokedAt == nil || !revoked.RevokedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("revoked_at = %v", revoked.RevokedAt)
	}

	keys, err := registry.ListKeys(ctx, "acme")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].ID != key.ID {
		t.Fatalf("keys = %+v", keys)
	}
	if _, err := registry.ListKeys(ctx, "missing"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("ListKeys(missing) = %v, want ErrNotFound", err)
	}
}
