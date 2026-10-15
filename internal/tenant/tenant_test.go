package tenant

import (
	"context"
	"errors"
	"testing"
)

// The default extension resolves to the configured schema, which is the
// single-tenant behaviour the service had before the abstraction existed.
func TestDefaultResolvesConfiguredSchema(t *testing.T) {
	ext := NewDefault("bank_data")
	resolved, err := ext.Authenticate(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Schema != "bank_data" {
		t.Fatalf("schema = %q, want the configured name", resolved.Schema)
	}
	tenants, err := ext.ListTenants(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tenants) != 1 || tenants[0].Schema != "bank_data" {
		t.Fatalf("tenants = %+v, want the one configured schema", tenants)
	}
	if ext.Schema() != "bank_data" {
		t.Fatalf("Schema() = %q", ext.Schema())
	}
}

// An unset schema falls back to the name the storage layer would itself have
// used, so a deployment that configures nothing keeps working.
func TestDefaultFallsBackToPublic(t *testing.T) {
	for _, input := range []string{"", "   "} {
		ext := NewDefault(input)
		if ext.Schema() != "public" {
			t.Errorf("NewDefault(%q).Schema() = %q, want public", input, ext.Schema())
		}
	}
}

// A surrounding space is trimmed rather than becoming part of the schema name,
// because the name is interpolated into every qualified table reference.
func TestDefaultTrimsSchemaName(t *testing.T) {
	if got := NewDefault("  tenant_a  ").Schema(); got != "tenant_a" {
		t.Fatalf("schema = %q, want the trimmed name", got)
	}
}

// The default extension does not inspect the request: it passes everything
// through, which is what "no authentication" means.
func TestDefaultIgnoresRequestData(t *testing.T) {
	ext := NewDefault("public")
	resolved, err := ext.Authenticate(context.Background(), Request{
		Credential: "anything",
		Headers:    map[string]string{"x-user-assertion": "someone"},
	})
	if err != nil {
		t.Fatalf("the default extension must not reject a request: %v", err)
	}
	if resolved.Schema != "public" {
		t.Fatalf("schema = %q", resolved.Schema)
	}
}

// An authentication failure is reported through the sentinel, so a caller can
// map it onto a response status without reading the message.
func TestAuthenticationErrorIsClassifiable(t *testing.T) {
	err := &AuthenticationError{Reason: "missing credential", Headers: map[string]string{"WWW-Authenticate": "Bearer"}}
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err = %v, want the sentinel class", err)
	}
	if err.Headers["WWW-Authenticate"] != "Bearer" {
		t.Fatalf("headers = %+v, want the challenge preserved", err.Headers)
	}
}

// A custom extension satisfies the interface, which is what makes the
// abstraction swappable without touching a caller.
func TestCustomExtensionSatisfiesInterface(t *testing.T) {
	var ext Extension = &Default{}
	if _, err := ext.ListTenants(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The default extension supplies no overrides, so a deployment that keys no
// configuration by tenant resolves exactly as it did before the layer existed.
func TestDefaultSuppliesNoOverrides(t *testing.T) {
	resolved, err := NewDefault("public").Authenticate(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Overrides != nil {
		t.Fatalf("overrides = %v, want none from the default extension", resolved.Overrides)
	}
}
