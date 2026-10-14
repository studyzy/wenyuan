package app

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The composition root resolves the tenant extension from the configured
// schema, so the schema the extension reports and the schema the storage layer
// qualifies its statements with cannot drift apart.
func TestTenantExtensionFollowsConfiguredSchema(t *testing.T) {
	ext := NewTenantExtension(config.Config{DatabaseSchema: "tenant_a"})
	resolved, err := ext.Authenticate(context.Background(), tenant.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Schema != "tenant_a" {
		t.Fatalf("schema = %q, want the configured schema", resolved.Schema)
	}
	tenants, err := ext.ListTenants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tenants) != 1 || tenants[0].Schema != "tenant_a" {
		t.Fatalf("tenants = %+v, want the one configured schema", tenants)
	}
}

// With no schema configured the extension reports the default the storage
// layer would itself have used, so an unconfigured deployment is unaffected.
func TestTenantExtensionDefaultsToPublic(t *testing.T) {
	ext := NewTenantExtension(config.Config{})
	resolved, err := ext.Authenticate(context.Background(), tenant.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Schema != "public" {
		t.Fatalf("schema = %q, want public", resolved.Schema)
	}
}
