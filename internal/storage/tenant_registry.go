package storage

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// TenantRegistry is the control-plane store for tenants and their credentials.
//
// It lives in the admin schema, not in a tenant schema, so it is deliberately
// separate from Store: a data-plane handle is bound to one tenant schema while
// the registry answers for all of them.
type TenantRegistry interface {
	CreateTenant(ctx context.Context, record tenant.Record) error
	GetTenant(ctx context.Context, id string) (tenant.Record, error)
	ListTenants(ctx context.Context, filter TenantFilter) ([]tenant.Record, error)
	UpdateTenant(ctx context.Context, id string, update TenantUpdate) (tenant.Record, error)
	DeleteTenant(ctx context.Context, id string, now time.Time) (tenant.Record, error)

	CreateKey(ctx context.Context, key tenant.Key) error
	ListKeys(ctx context.Context, tenantID string) ([]tenant.Key, error)
	RevokeKey(ctx context.Context, tenantID, keyID string, now time.Time) error
	LookupKey(ctx context.Context, hash []byte) (tenant.Key, tenant.Record, error)
	TouchKey(ctx context.Context, keyID string, now time.Time) error
}

// TenantFilter selects the registry rows a list call returns. The zero value
// lists every tenant in id order.
type TenantFilter struct {
	// Status, when set, returns only tenants in that state.
	Status tenant.Status
	// Query matches the tenant id or display name, case-insensitively.
	Query string
	// Limit caps the result; zero means no cap.
	Limit int
	// Offset skips that many rows after filtering and sorting.
	Offset int
}

// TenantUpdate is a partial update. A nil member is left unchanged; a non-nil
// Config replaces the stored object rather than merging into it.
type TenantUpdate struct {
	DisplayName *string
	Config      map[string]any
	Status      *tenant.Status
}
