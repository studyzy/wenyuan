package app

import (
	"context"
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
type RegistryExtension struct {
	registry storage.TenantRegistry
}

// NewRegistryExtension builds the extension over a registry.
func NewRegistryExtension(registry storage.TenantRegistry) *RegistryExtension {
	return &RegistryExtension{registry: registry}
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
