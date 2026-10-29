package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// PostgresTenantRegistry stores the control plane's tenants and keys in the
// admin schema. It is separate from PostgresStore because that store is bound
// to one tenant schema, while the registry answers for all of them.
type PostgresTenantRegistry struct {
	db     DB
	schema string
}

// NewPostgresTenantRegistry returns a registry over the fixed admin schema.
func NewPostgresTenantRegistry(db DB) *PostgresTenantRegistry {
	return &PostgresTenantRegistry{db: db, schema: AdminSchema}
}

const tenantColumns = "tenant_id, display_name, status, config, created_at, updated_at, deleted_at"

const keyColumns = "key_id::text, tenant_id, key_hash, prefix, label, ephemeral, expires_at, revoked_at, last_used_at, created_at"

// qualified names one registry table in this registry's schema.
func (r *PostgresTenantRegistry) qualified(table string) string {
	return quoteIdent(r.schema) + "." + quoteIdent(table)
}

// CreateTenant inserts a tenant. A duplicate id is ErrExists.
func (r *PostgresTenantRegistry) CreateTenant(ctx context.Context, record tenant.Record) error {
	if err := tenant.ValidateID(record.ID); err != nil {
		return err
	}
	if record.Status == "" {
		record.Status = tenant.StatusProvisioning
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	config, err := marshalTenantConfig(record.Config)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx,
		"INSERT INTO "+r.qualified("tenant")+" (tenant_id, display_name, status, config, created_at, updated_at) VALUES ($1,$2,$3,$4::jsonb,$5,$6)",
		record.ID, record.DisplayName, string(record.Status), config, record.CreatedAt, record.UpdatedAt)
	if isUniqueViolation(err) {
		return tenant.ErrExists
	}
	return err
}

// GetTenant returns one tenant by id.
func (r *PostgresTenantRegistry) GetTenant(ctx context.Context, id string) (tenant.Record, error) {
	record, err := scanTenantRecord(r.db.QueryRowContext(ctx,
		"SELECT "+tenantColumns+" FROM "+r.qualified("tenant")+" WHERE tenant_id = $1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.Record{}, tenant.ErrNotFound
	}
	return record, err
}

// ListTenants returns the tenants matching filter, ordered by id.
func (r *PostgresTenantRegistry) ListTenants(ctx context.Context, filter TenantFilter) ([]tenant.Record, error) {
	var where []string
	var args []any
	if filter.Status != "" {
		args = append(args, string(filter.Status))
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		args = append(args, "%"+escapeLike(query)+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(tenant_id ILIKE $%d ESCAPE '\\' OR display_name ILIKE $%d ESCAPE '\\')", n, n))
	}
	statement := "SELECT " + tenantColumns + " FROM " + r.qualified("tenant")
	if len(where) > 0 {
		statement += " WHERE " + strings.Join(where, " AND ")
	}
	statement += " ORDER BY tenant_id"
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		statement += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	if filter.Offset > 0 {
		args = append(args, filter.Offset)
		statement += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	rows, err := r.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := make([]tenant.Record, 0)
	for rows.Next() {
		record, err := scanTenantRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// UpdateTenant applies a partial update under a row lock.
func (r *PostgresTenantRegistry) UpdateTenant(ctx context.Context, id string, update TenantUpdate) (tenant.Record, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return tenant.Record{}, err
	}
	defer func() { _ = tx.Rollback() }()

	record, err := scanTenantRecord(tx.QueryRowContext(ctx,
		"SELECT "+tenantColumns+" FROM "+r.qualified("tenant")+" WHERE tenant_id = $1 FOR UPDATE", id))
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.Record{}, tenant.ErrNotFound
	}
	if err != nil {
		return tenant.Record{}, err
	}
	if update.DisplayName != nil {
		record.DisplayName = *update.DisplayName
	}
	if update.Config != nil {
		record.Config = update.Config
	}
	now := time.Now().UTC()
	if update.Status != nil && *update.Status != record.Status {
		record, err = tenant.Transition(record, *update.Status, now)
		if err != nil {
			return tenant.Record{}, err
		}
	} else {
		record.UpdatedAt = now
	}
	if err := r.updateTenantTx(ctx, tx, record); err != nil {
		return tenant.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return tenant.Record{}, err
	}
	return record, nil
}

// DeleteTenant soft-deletes a tenant. Repeating the delete is a no-op.
func (r *PostgresTenantRegistry) DeleteTenant(ctx context.Context, id string, now time.Time) (tenant.Record, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return tenant.Record{}, err
	}
	defer func() { _ = tx.Rollback() }()

	record, err := scanTenantRecord(tx.QueryRowContext(ctx,
		"SELECT "+tenantColumns+" FROM "+r.qualified("tenant")+" WHERE tenant_id = $1 FOR UPDATE", id))
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.Record{}, tenant.ErrNotFound
	}
	if err != nil {
		return tenant.Record{}, err
	}
	if record.Status == tenant.StatusDeleted {
		return record, nil
	}
	record, err = tenant.Transition(record, tenant.StatusDeleted, now)
	if err != nil {
		return tenant.Record{}, err
	}
	if err := r.updateTenantTx(ctx, tx, record); err != nil {
		return tenant.Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return tenant.Record{}, err
	}
	return record, nil
}

// updateTenantTx writes every mutable member of a record, so the caller's
// in-memory copy is exactly what the row holds.
func (r *PostgresTenantRegistry) updateTenantTx(ctx context.Context, tx Tx, record tenant.Record) error {
	config, err := marshalTenantConfig(record.Config)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"UPDATE "+r.qualified("tenant")+" SET display_name=$2, status=$3, config=$4::jsonb, updated_at=$5, deleted_at=$6 WHERE tenant_id=$1",
		record.ID, record.DisplayName, string(record.Status), config, record.UpdatedAt, nullableTime(record.DeletedAt))
	return err
}

// CreateKey files a credential under its tenant.
func (r *PostgresTenantRegistry) CreateKey(ctx context.Context, key tenant.Key) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO "+r.qualified("tenant_key")+" (key_id, tenant_id, key_hash, prefix, label, ephemeral, expires_at, revoked_at, last_used_at, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
		key.ID, key.TenantID, key.Hash, key.Prefix, key.Label, key.Ephemeral,
		nullableTime(key.ExpiresAt), nullableTime(key.RevokedAt), nullableTime(key.LastUsedAt), key.CreatedAt)
	if isUniqueViolation(err) {
		return tenant.ErrExists
	}
	if isForeignKeyViolation(err) {
		return tenant.ErrNotFound
	}
	return err
}

// ListKeys returns a tenant's keys, oldest first.
func (r *PostgresTenantRegistry) ListKeys(ctx context.Context, tenantID string) ([]tenant.Key, error) {
	var one int
	err := r.db.QueryRowContext(ctx, "SELECT 1 FROM "+r.qualified("tenant")+" WHERE tenant_id = $1", tenantID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tenant.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx,
		"SELECT "+keyColumns+" FROM "+r.qualified("tenant_key")+" WHERE tenant_id = $1 ORDER BY created_at, key_id::text", tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	keys := make([]tenant.Key, 0)
	for rows.Next() {
		key, err := scanTenantKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// RevokeKey stamps revoked_at. Repeating the revoke keeps the first stamp.
func (r *PostgresTenantRegistry) RevokeKey(ctx context.Context, tenantID, keyID string, now time.Time) error {
	result, err := r.db.ExecContext(ctx,
		"UPDATE "+r.qualified("tenant_key")+" SET revoked_at = COALESCE(revoked_at, $3) WHERE tenant_id = $1 AND key_id = $2",
		tenantID, keyID, now)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return tenant.ErrNotFound
	}
	return nil
}

// LookupKey resolves a key hash to its credential and tenant. A revoked or
// expired key is returned as-is; the caller decides whether to reject it.
func (r *PostgresTenantRegistry) LookupKey(ctx context.Context, hash []byte) (tenant.Key, tenant.Record, error) {
	row := r.db.QueryRowContext(ctx,
		"SELECT k.key_id::text, k.tenant_id, k.key_hash, k.prefix, k.label, k.ephemeral, k.expires_at, k.revoked_at, k.last_used_at, k.created_at, "+
			"t.tenant_id, t.display_name, t.status, t.config, t.created_at, t.updated_at, t.deleted_at"+
			" FROM "+r.qualified("tenant_key")+" k JOIN "+r.qualified("tenant")+" t ON t.tenant_id = k.tenant_id"+
			" WHERE k.key_hash = $1", hash)

	var key tenant.Key
	var record tenant.Record
	var status string
	var config []byte
	var expires, revoked, lastUsed, deleted sql.NullTime
	err := row.Scan(
		&key.ID, &key.TenantID, &key.Hash, &key.Prefix, &key.Label, &key.Ephemeral,
		&expires, &revoked, &lastUsed, &key.CreatedAt,
		&record.ID, &record.DisplayName, &status, &config, &record.CreatedAt, &record.UpdatedAt, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return tenant.Key{}, tenant.Record{}, tenant.ErrNotFound
	}
	if err != nil {
		return tenant.Key{}, tenant.Record{}, err
	}
	key.ExpiresAt = timePointer(expires)
	key.RevokedAt = timePointer(revoked)
	key.LastUsedAt = timePointer(lastUsed)
	record.Status = tenant.Status(status)
	if len(config) > 0 {
		if err := json.Unmarshal(config, &record.Config); err != nil {
			return tenant.Key{}, tenant.Record{}, fmt.Errorf("decode tenant config: %w", err)
		}
	}
	if record.Config == nil {
		record.Config = map[string]any{}
	}
	record.DeletedAt = timePointer(deleted)
	return key, record, nil
}

// TouchKey records a key's last use.
func (r *PostgresTenantRegistry) TouchKey(ctx context.Context, keyID string, now time.Time) error {
	result, err := r.db.ExecContext(ctx,
		"UPDATE "+r.qualified("tenant_key")+" SET last_used_at = $2 WHERE key_id = $1", keyID, now)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return tenant.ErrNotFound
	}
	return nil
}

// scanTenantRecord reads the tenantColumns list.
func scanTenantRecord(row rowScanner) (tenant.Record, error) {
	var record tenant.Record
	var status string
	var config []byte
	var deleted sql.NullTime
	if err := row.Scan(&record.ID, &record.DisplayName, &status, &config, &record.CreatedAt, &record.UpdatedAt, &deleted); err != nil {
		return tenant.Record{}, err
	}
	record.Status = tenant.Status(status)
	if len(config) > 0 {
		if err := json.Unmarshal(config, &record.Config); err != nil {
			return tenant.Record{}, fmt.Errorf("decode tenant config: %w", err)
		}
	}
	if record.Config == nil {
		record.Config = map[string]any{}
	}
	record.DeletedAt = timePointer(deleted)
	return record, nil
}

// scanTenantKey reads the keyColumns list.
func scanTenantKey(row rowScanner) (tenant.Key, error) {
	var key tenant.Key
	var expires, revoked, lastUsed sql.NullTime
	if err := row.Scan(&key.ID, &key.TenantID, &key.Hash, &key.Prefix, &key.Label, &key.Ephemeral, &expires, &revoked, &lastUsed, &key.CreatedAt); err != nil {
		return tenant.Key{}, err
	}
	key.ExpiresAt = timePointer(expires)
	key.RevokedAt = timePointer(revoked)
	key.LastUsedAt = timePointer(lastUsed)
	return key, nil
}

func marshalTenantConfig(config map[string]any) (string, error) {
	if config == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode tenant config: %w", err)
	}
	return string(raw), nil
}

func timePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	copied := value.Time
	return &copied
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func escapeLike(value string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)
}

func isForeignKeyViolation(err error) bool {
	var state interface{ SQLState() string }
	if !errors.As(err, &state) {
		return false
	}
	return state.SQLState() == "23503"
}
