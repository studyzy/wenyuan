//go:build pgx

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

func TestPostgresTenantRegistryLifecycle(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the tenant registry test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("admin_e2e_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
		_ = db.Close()
	})

	if err := migrateAdminSchema(ctx, db, schema); err != nil {
		t.Fatalf("migrate admin schema: %v", err)
	}
	if err := migrateAdminSchema(ctx, db, schema); err != nil {
		t.Fatalf("second migrate should be idempotent: %v", err)
	}

	registry := &PostgresTenantRegistry{db: db, schema: schema}
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
	if got.Config["enable_text_search"] != false {
		t.Fatalf("config = %+v", got.Config)
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

	deleted, err := registry.DeleteTenant(ctx, "acme", now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if deleted.Status != tenant.StatusDeleted || deleted.DeletedAt == nil {
		t.Fatalf("deleted = %+v", deleted)
	}
	if _, err := registry.DeleteTenant(ctx, "acme", now.Add(4*time.Minute)); err != nil {
		t.Fatalf("second DeleteTenant should be idempotent: %v", err)
	}
}
