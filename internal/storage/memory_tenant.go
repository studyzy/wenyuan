package storage

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// MemoryTenantRegistry is the in-process registry used by tests and by a
// deployment that runs the control plane without PostgreSQL.
type MemoryTenantRegistry struct {
	mu      sync.RWMutex
	tenants map[string]tenant.Record
	keys    map[string]tenant.Key
}

// NewMemoryTenantRegistry returns an empty registry.
func NewMemoryTenantRegistry() *MemoryTenantRegistry {
	return &MemoryTenantRegistry{
		tenants: make(map[string]tenant.Record),
		keys:    make(map[string]tenant.Key),
	}
}

// CreateTenant inserts a tenant. A duplicate id is ErrExists.
func (r *MemoryTenantRegistry) CreateTenant(_ context.Context, record tenant.Record) error {
	if err := tenant.ValidateID(record.ID); err != nil {
		return err
	}
	if record.Status == "" {
		record.Status = tenant.StatusProvisioning
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tenants[record.ID]; ok {
		return tenant.ErrExists
	}
	r.tenants[record.ID] = copyTenantRecord(record)
	return nil
}

// GetTenant returns one tenant by id.
func (r *MemoryTenantRegistry) GetTenant(_ context.Context, id string) (tenant.Record, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, ok := r.tenants[id]
	if !ok {
		return tenant.Record{}, tenant.ErrNotFound
	}
	return copyTenantRecord(record), nil
}

// ListTenants returns the tenants matching filter, ordered by id.
func (r *MemoryTenantRegistry) ListTenants(_ context.Context, filter TenantFilter) ([]tenant.Record, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	matched := make([]tenant.Record, 0, len(r.tenants))
	for _, record := range r.tenants {
		if filter.Status != "" && record.Status != filter.Status {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(record.ID), query) &&
			!strings.Contains(strings.ToLower(record.DisplayName), query) {
			continue
		}
		matched = append(matched, copyTenantRecord(record))
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].ID < matched[j].ID })
	return pageRecords(matched, filter.Limit, filter.Offset), nil
}

// UpdateTenant applies a partial update and returns the stored record.
func (r *MemoryTenantRegistry) UpdateTenant(_ context.Context, id string, update TenantUpdate) (tenant.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.tenants[id]
	if !ok {
		return tenant.Record{}, tenant.ErrNotFound
	}
	if update.DisplayName != nil {
		record.DisplayName = *update.DisplayName
	}
	if update.Config != nil {
		record.Config = copyConfig(update.Config)
	}
	if update.Status != nil && *update.Status != record.Status {
		next, err := tenant.Transition(record, *update.Status, time.Now().UTC())
		if err != nil {
			return tenant.Record{}, err
		}
		record = next
	} else {
		record.UpdatedAt = time.Now().UTC()
	}
	r.tenants[id] = copyTenantRecord(record)
	return copyTenantRecord(record), nil
}

// DeleteTenant soft-deletes a tenant. Repeating the delete is a no-op.
func (r *MemoryTenantRegistry) DeleteTenant(_ context.Context, id string, now time.Time) (tenant.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.tenants[id]
	if !ok {
		return tenant.Record{}, tenant.ErrNotFound
	}
	if record.Status == tenant.StatusDeleted {
		return copyTenantRecord(record), nil
	}
	next, err := tenant.Transition(record, tenant.StatusDeleted, now)
	if err != nil {
		return tenant.Record{}, err
	}
	r.tenants[id] = copyTenantRecord(next)
	return copyTenantRecord(next), nil
}

// CreateKey files a credential under its tenant.
func (r *MemoryTenantRegistry) CreateKey(_ context.Context, key tenant.Key) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tenants[key.TenantID]; !ok {
		return tenant.ErrNotFound
	}
	if _, ok := r.keys[key.ID]; ok {
		return tenant.ErrExists
	}
	r.keys[key.ID] = copyTenantKey(key)
	return nil
}

// ListKeys returns a tenant's keys, oldest first.
func (r *MemoryTenantRegistry) ListKeys(_ context.Context, tenantID string) ([]tenant.Key, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.tenants[tenantID]; !ok {
		return nil, tenant.ErrNotFound
	}
	keys := make([]tenant.Key, 0)
	for _, key := range r.keys {
		if key.TenantID == tenantID {
			keys = append(keys, copyTenantKey(key))
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if !keys[i].CreatedAt.Equal(keys[j].CreatedAt) {
			return keys[i].CreatedAt.Before(keys[j].CreatedAt)
		}
		return keys[i].ID < keys[j].ID
	})
	return keys, nil
}

// RevokeKey stamps revoked_at. Repeating the revoke keeps the first stamp.
func (r *MemoryTenantRegistry) RevokeKey(_ context.Context, tenantID, keyID string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[keyID]
	if !ok || key.TenantID != tenantID {
		return tenant.ErrNotFound
	}
	if key.RevokedAt != nil {
		return nil
	}
	revokedAt := now
	key.RevokedAt = &revokedAt
	r.keys[keyID] = key
	return nil
}

// LookupKey resolves a key hash to its credential and tenant. A revoked or
// expired key is returned as-is; the caller decides whether to reject it.
func (r *MemoryTenantRegistry) LookupKey(_ context.Context, hash []byte) (tenant.Key, tenant.Record, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, key := range r.keys {
		if !bytes.Equal(key.Hash, hash) {
			continue
		}
		record, ok := r.tenants[key.TenantID]
		if !ok {
			return tenant.Key{}, tenant.Record{}, tenant.ErrNotFound
		}
		return copyTenantKey(key), copyTenantRecord(record), nil
	}
	return tenant.Key{}, tenant.Record{}, tenant.ErrNotFound
}

// TouchKey records a key's last use.
func (r *MemoryTenantRegistry) TouchKey(_ context.Context, keyID string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key, ok := r.keys[keyID]
	if !ok {
		return tenant.ErrNotFound
	}
	lastUsed := now
	key.LastUsedAt = &lastUsed
	r.keys[keyID] = key
	return nil
}

// copyTenantRecord deep-copies the members a caller could mutate.
func copyTenantRecord(record tenant.Record) tenant.Record {
	record.Config = copyConfig(record.Config)
	if record.DeletedAt != nil {
		deletedAt := *record.DeletedAt
		record.DeletedAt = &deletedAt
	}
	return record
}

// copyTenantKey deep-copies the members a caller could mutate.
func copyTenantKey(key tenant.Key) tenant.Key {
	key.Hash = append([]byte(nil), key.Hash...)
	key.ExpiresAt = copyTimePointer(key.ExpiresAt)
	key.RevokedAt = copyTimePointer(key.RevokedAt)
	key.LastUsedAt = copyTimePointer(key.LastUsedAt)
	return key
}

func copyConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	out := make(map[string]any, len(config))
	for key, value := range config {
		out[key] = value
	}
	return out
}

func copyTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func pageRecords(records []tenant.Record, limit, offset int) []tenant.Record {
	if offset > 0 {
		if offset >= len(records) {
			return nil
		}
		records = records[offset:]
	}
	if limit > 0 && limit < len(records) {
		records = records[:limit]
	}
	return records
}
