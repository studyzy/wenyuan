package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrSchemaExists reports a provisioning request for a schema that is already
// there without the explicit adopt flag.
var ErrSchemaExists = errors.New("schema already exists")

// SchemaExists reports whether PostgreSQL already holds a schema with this
// name.
func SchemaExists(ctx context.Context, db DB, schema string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&exists)
	return exists, err
}

// SchemaStoreConfig is the part of a deployment's store configuration a
// per-tenant schema migration needs, so a tenant's schema is built exactly like
// the process schema it is served alongside.
//
// It is separate from BackendConfig because provisioning is not opening a
// backend: the caller already holds the database. The zero value is the
// reference's default backend — native tsvector, pgvector.
type SchemaStoreConfig struct {
	VectorExtension          string
	TextSearchExtension      string
	PGFunctionSchema         string
	PGTokenizer              string
	TextSearchNativeLanguage string
}

// ProvisionTenantSchema creates and migrates a tenant schema, or adopts an
// existing one when the caller asked for it.
//
// An existing schema is never taken over implicitly: any pre-existing schema —
// complete, empty or partial — requires adopt. The migration itself then
// decides whether the schema is complete (adopt), empty (run the series) or
// partial (refuse with the missing objects named).
//
// The text-search settings travel with it because they are not part of the
// series: the pg_search keyword index is built by Migrate after it commits, and
// only when the store knows which backend the deployment selected. A store
// built without them treats every schema as native and silently skips the
// index, which is what a tenant created under this function used to get — a
// schema whose keyword arm then failed at query time with "memory_units does
// not contain a USING paradedb index".
func ProvisionTenantSchema(ctx context.Context, db DB, schema string, adopt bool, storeCfg SchemaStoreConfig) error {
	if !validSchemaName(schema) {
		return fmt.Errorf("%w: %q", ErrInvalidSchema, schema)
	}
	exists, err := SchemaExists(ctx, db, schema)
	if err != nil {
		return err
	}
	if exists && !adopt {
		return fmt.Errorf("%w: %q", ErrSchemaExists, schema)
	}
	return migrateSchemaStore(ctx, db, schema, storeCfg)
}

// migrateSchemaStore binds a store to one schema with the deployment's backend
// settings and runs the series.
func migrateSchemaStore(ctx context.Context, db DB, schema string, storeCfg SchemaStoreConfig) error {
	store := NewPostgresStore(db, schema)
	store.vectorExtension = storeCfg.VectorExtension
	store.textSearchExtension = storeCfg.TextSearchExtension
	store.pgFunctionSchema = storeCfg.PGFunctionSchema
	store.pgTokenizer = storeCfg.PGTokenizer
	store.tsNativeLanguage = storeCfg.TextSearchNativeLanguage
	return store.Migrate(ctx)
}

// NewMultiTenantPostgresStore returns a store that requires every statement's
// schema from the request context. It is what the composition root builds when
// the deployment runs in multi-tenant mode; tests use it to exercise the same
// contract without going through BackendConfig.
func NewMultiTenantPostgresStore(db DB, defaultSchema string) *PostgresStore {
	store := NewPostgresStore(db, defaultSchema)
	store.requireSchema = true
	return store
}

var (
	// ErrInvalidSchema reports a schema name that cannot be used as a
	// PostgreSQL identifier.
	ErrInvalidSchema = errors.New("invalid schema name")
	// ErrSchemaRequired reports a multi-tenant store asked for a schema on a
	// context that carries none. It is deliberately not a fallback: falling
	// back would read or write the process schema for a tenant request.
	ErrSchemaRequired = errors.New("tenant schema is required on the context")
)

// schemaPattern is the same shape the tenant registry validates. Storage keeps
// its own copy because the package boundary forbids importing the tenant
// package; the two must stay identical. Letters may be upper or lower case:
// every statement quotes the name, so PostgreSQL preserves the case.
var schemaPattern = regexp.MustCompile("^[A-Za-z][A-Za-z0-9_]{1,62}$")

// schemaContextKey keys the resolved tenant schema on a request context.
type schemaContextKey struct{}

// WithSchema records the schema every statement of the operation must use. An
// empty or malformed name is refused rather than stored, so a later query can
// never interpolate an unexpected identifier.
func WithSchema(ctx context.Context, schema string) (context.Context, error) {
	if !validSchemaName(schema) {
		return ctx, fmt.Errorf("%w: %q", ErrInvalidSchema, schema)
	}
	return context.WithValue(ctx, schemaContextKey{}, schema), nil
}

// SchemaFrom returns the schema a context carries, if any.
func SchemaFrom(ctx context.Context) (string, bool) {
	schema, ok := ctx.Value(schemaContextKey{}).(string)
	return schema, ok && schema != ""
}

// validSchemaName accepts the id shape and refuses the names that must never
// receive tenant traffic: the registry schema, the system catalog and
// PostgreSQL's reserved namespace prefix. The reserved comparison ignores case
// because an unquoted spelling folds to lowercase in SQL.
func validSchemaName(schema string) bool {
	if !schemaPattern.MatchString(schema) {
		return false
	}
	lowered := strings.ToLower(schema)
	if lowered == "admin" || lowered == "information_schema" || strings.HasPrefix(lowered, "pg_") {
		return false
	}
	return true
}

// schemaFor resolves the schema one statement must be qualified with.
//
// A single-tenant store has one schema and uses it; a multi-tenant store
// requires the request context to name one and fails closed when it does not.
// The distinction is what keeps a missing middleware or a background call
// without a schema from silently reaching the process schema.
func (s *PostgresStore) schemaFor(ctx context.Context) (string, error) {
	if schema, ok := SchemaFrom(ctx); ok {
		return schema, nil
	}
	if !s.requireSchema {
		return s.defaultSchema, nil
	}
	return "", ErrSchemaRequired
}

// ErrSchemaProtected reports a purge or migration refused because the schema is
// shared by the deployment: the extension host or the registry.
var ErrSchemaProtected = errors.New("schema is protected")

// DropTenantSchema removes a tenant schema and everything in it.
//
// public and admin are refused before any DDL: public hosts the extensions
// every tenant's tables depend on, and admin holds the registry itself. The
// advisory lock serializes the drop with provisioning, so a schema cannot be
// dropped while another process is migrating it.
func DropTenantSchema(ctx context.Context, db DB, schema string) error {
	if schema == AdminSchema || schema == extensionSchema {
		return fmt.Errorf("%w: %q", ErrSchemaProtected, schema)
	}
	if !validSchemaName(schema) {
		return fmt.Errorf("%w: %q", ErrInvalidSchema, schema)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('wenyuan:tenant-schema'))"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE"); err != nil {
		return fmt.Errorf("drop tenant schema %q: %w", schema, err)
	}
	return tx.Commit()
}

// MigrateTenantSchema brings one tenant schema to the current reference series.
// A complete schema is adopted, an empty one is built, and a partial one is
// refused with the missing objects named. public is allowed (an adopted
// single-tenant deployment lives there); admin never is.
//
// It carries the deployment's backend settings for the same reason
// ProvisionTenantSchema does: the pg_search keyword index is reconciled after
// the series commits, and a store that does not know the backend skips it.
func MigrateTenantSchema(ctx context.Context, db DB, schema string, storeCfg SchemaStoreConfig) error {
	if schema == AdminSchema {
		return fmt.Errorf("%w: %q is the registry", ErrSchemaProtected, schema)
	}
	if !validSchemaName(schema) {
		return fmt.Errorf("%w: %q", ErrInvalidSchema, schema)
	}
	return migrateSchemaStore(ctx, db, schema, storeCfg)
}
