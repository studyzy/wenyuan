package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// RegistryExtension resolves a request's credential to a tenant through the
// control-plane registry. It is the production tenant.Extension: the default
// extension serves one configured schema and never inspects the request, while
// this one maps every key to the schema it owns.
//
// An operator credential is a second path: the management plane's key, which
// owns every tenant, resolves the tenant it names and no other. That is what
// lets the console enter a tenant without minting a key; the key path is
// unchanged, so an ordinary tenant key still resolves only its own tenant and
// ignores the named one.
type RegistryExtension struct {
	registry    storage.TenantRegistry
	operatorKey string
}

// NewRegistryExtension builds the extension over a registry. An empty
// operatorKey disables operator impersonation, which is what a background
// process gets: it never serves a request and needs no operator credential.
func NewRegistryExtension(registry storage.TenantRegistry, operatorKey string) *RegistryExtension {
	return &RegistryExtension{registry: registry, operatorKey: strings.TrimSpace(operatorKey)}
}

var _ tenant.Extension = (*RegistryExtension)(nil)

// Authenticate maps a key hash to its tenant and returns the schema every
// statement of the request must use.
//
// Missing, unknown, revoked, expired and inactive are all the same
// AuthenticationError: the response body must not tell a caller which of them
// it hit. A registry failure is returned raw so the middleware can answer 503
// instead of pretending the credential was wrong.
func (e *RegistryExtension) Authenticate(ctx context.Context, request tenant.Request) (tenant.Context, error) {
	credential := strings.TrimSpace(request.Credential)
	if credential == "" {
		return tenant.Context{}, &tenant.AuthenticationError{Reason: "missing credential"}
	}
	if e.isOperator(credential) {
		return e.authenticateOperator(ctx, request.TenantID)
	}
	key, record, err := e.registry.LookupKey(ctx, tenant.HashKey(credential))
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return tenant.Context{}, &tenant.AuthenticationError{Reason: "unknown credential"}
		}
		return tenant.Context{}, err
	}
	now := time.Now().UTC()
	switch {
	case key.RevokedAt != nil:
		return tenant.Context{}, &tenant.AuthenticationError{Reason: "revoked credential"}
	case key.ExpiresAt != nil && !key.ExpiresAt.After(now):
		return tenant.Context{}, &tenant.AuthenticationError{Reason: "expired credential"}
	case record.Status != tenant.StatusActive:
		return tenant.Context{}, &tenant.AuthenticationError{Reason: "tenant is " + string(record.Status)}
	}
	return tenant.Context{ID: record.ID, Schema: record.ID, Overrides: record.Config}, nil
}

// isOperator reports whether the credential is the management plane's key. The
// comparison is constant-time for the same reason the key lookup is: the
// caller learns only whether it matched.
func (e *RegistryExtension) isOperator(credential string) bool {
	if e.operatorKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(credential), []byte(e.operatorKey)) == 1
}

// authenticateOperator resolves the tenant an operator names through the
// registry. There is deliberately no default: an operator credential without a
// tenant is not a request for the deployment's own schema.
func (e *RegistryExtension) authenticateOperator(ctx context.Context, tenantID string) (tenant.Context, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return tenant.Context{}, &tenant.AuthenticationError{Reason: "operator credential names no tenant"}
	}
	record, err := e.registry.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return tenant.Context{}, &tenant.AuthenticationError{Reason: "unknown tenant"}
		}
		return tenant.Context{}, err
	}
	if record.Status != tenant.StatusActive {
		return tenant.Context{}, &tenant.AuthenticationError{Reason: "tenant is " + string(record.Status)}
	}
	return tenant.Context{ID: record.ID, Schema: record.ID, Overrides: record.Config}, nil
}

// ListTenants returns the active tenants background work should cover.
func (e *RegistryExtension) ListTenants(ctx context.Context) ([]tenant.Tenant, error) {
	records, err := e.registry.ListTenants(ctx, storage.TenantFilter{Status: tenant.StatusActive})
	if err != nil {
		return nil, err
	}
	tenants := make([]tenant.Tenant, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.ID) == "" {
			continue
		}
		tenants = append(tenants, tenant.Tenant{
			Schema:    record.ID,
			ID:        record.ID,
			Overrides: record.Config,
		})
	}
	return tenants, nil
}
