package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrDriverUnavailable is returned when the PostgreSQL backend is selected but
// no database driver has been registered.
var ErrDriverUnavailable = errors.New("no database driver registered; build the pgx driver file to enable PostgreSQL")

// PostgresBackend opens a PostgreSQL-backed Store.
type PostgresBackend struct{}

// Open connects to PostgreSQL using the configured URL and schema. The actual
// driver is supplied through the registered OpenDBFunc so this file carries no
// third-party import.
func (b *PostgresBackend) Open(ctx context.Context, cfg BackendConfig) (Store, error) {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return nil, errors.New("database url is required for the postgres backend")
	}
	if !DriverAvailable() {
		return nil, ErrDriverUnavailable
	}
	db, err := openDB(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	schema := cfg.Schema
	if strings.TrimSpace(schema) == "" {
		schema = "public"
	}
	return &PostgresStore{db: db, schema: schema}, nil
}

// PostgresStore is the database-backed Store. It is the sole owner of the
// memory-domain tables.
type PostgresStore struct {
	db     DB
	schema string
	// objectStore, when set, holds the attachment and archive payloads
	// outside the database. It is an interface so the storage area carries no
	// object-store import, and the composition root supplies the
	// implementation.
	objectStore ObjectStore
}

// ObjectStore persists opaque payloads outside the database.
//
// It is declared here rather than importing an implementation so the storage
// area owns the shape it needs while the object-store package owns the
// protocol.
type ObjectStore interface {
	// Put stores the payload under the key, replacing any existing object.
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// Get returns the payload stored under the key, or ErrObjectNotFound.
	Get(ctx context.Context, key string) ([]byte, string, error)
}

// ErrObjectNotFound is the object-store miss, translated into the storage
// area's own sentinel so a caller sees ErrStoredFileNotFound either way.
var ErrObjectNotFound = errors.New("object not found")

// WithObjectStore returns the store with an external payload backend
// attached, so attachment and archive bytes live in the object store instead
// of the database.
func (s *PostgresStore) WithObjectStore(objectStore ObjectStore) *PostgresStore {
	s.objectStore = objectStore
	return s
}

// UsesObjectStore reports whether payloads are kept outside the database.
func (s *PostgresStore) UsesObjectStore() bool { return s.objectStore != nil }

// Schema returns the configured schema target.
func (s *PostgresStore) Schema() string { return s.schema }

// Close releases the database handle.
func (s *PostgresStore) Close() error { return s.db.Close() }

// Ready reports database reachability for readiness probing.
func (s *PostgresStore) Ready(ctx context.Context) error {
	return s.db.Ping(ctx)
}

// Ping satisfies the monitoring dependency contract.
func (s *PostgresStore) Ping(ctx context.Context) error { return s.Ready(ctx) }

// Migrate applies the ordered, idempotent migration series.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	series, err := MigrationSeries()
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdent(s.schema))); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s", quoteIdent(s.schema))); err != nil {
		return err
	}
	if err := s.ensureExtension(ctx, "vector"); err != nil {
		return err
	}
	// The trigram extension backs fuzzy entity-name resolution. A deployment
	// that cannot install it still serves: the resolver falls back to exact
	// matching when the probe fails.
	if err := s.ensureExtension(ctx, "pg_trgm"); err != nil {
		return err
	}
	if err := s.ensureMigrationTable(ctx); err != nil {
		return err
	}
	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}
	for _, migration := range series {
		if applied[migration.Version] {
			// A migration already recorded is not run again, so a start is
			// not a full replay and a destructive statement cannot repeat.
			continue
		}
		if err := s.applyMigration(ctx, migration); err != nil {
			return err
		}
	}
	return nil
}

// ensureMigrationTable creates the record of applied migrations.
//
// Without it every start replays the whole series, which is both wasted work
// and a hazard: a migration is only idempotent while someone remembers to write
// it that way.
func (s *PostgresStore) ensureMigrationTable(ctx context.Context) error {
	query := `CREATE TABLE IF NOT EXISTS ` + quoteIdent(s.schema) + `.schema_migrations (
	              version     INTEGER PRIMARY KEY,
	              name        TEXT NOT NULL,
	              applied_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
	          )`
	_, err := s.db.ExecContext(ctx, query)
	return err
}

// appliedMigrations returns the versions already recorded.
func (s *PostgresStore) appliedMigrations(ctx context.Context) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT version FROM `+quoteIdent(s.schema)+`.schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

// applyMigration runs one migration's statements and records it.
//
// The statements share a transaction, so a migration that fails halfway leaves
// no partial schema behind and is not recorded as applied.
func (s *PostgresStore) applyMigration(ctx context.Context, migration Migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, statement := range migration.Statements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %04d failed: %w", migration.Version, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(s.schema)+`.schema_migrations (version, name) VALUES ($1, $2)
		 ON CONFLICT (version) DO NOTHING`,
		migration.Version, migration.Name); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("recording migration %04d failed: %w", migration.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing migration %04d failed: %w", migration.Version, err)
	}
	return nil
}

// ensureExtension installs a named extension if it is missing and puts its
// schema on the search path, so an object using the extension resolves
// regardless of which schema actually holds it.
//
// The extension is installed in the schema that already has it when present,
// and otherwise in the first schema on the search path. Installing it into this
// store's own schema would hide it from a deployment whose extension lives
// elsewhere.
func (s *PostgresStore) ensureExtension(ctx context.Context, name string) error {
	var installed string
	probe := `SELECT n.nspname FROM pg_extension e
	          JOIN pg_namespace n ON n.oid = e.extnamespace
	          WHERE e.extname = $1`
	err := s.db.QueryRowContext(ctx, probe, name).Scan(&installed)
	switch {
	case err == nil && installed != "":
		// Already present somewhere; make it visible.
		_, err := s.db.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s, %s", quoteIdent(s.schema), quoteIdent(installed)))
		return err
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

	// Not installed: place it in the first schema already on the path, which is
	// this store's schema, and then make that schema visible to the table.
	if _, err := s.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS `+quoteIdent(name)); err != nil {
		return fmt.Errorf("install %s extension: %w", name, err)
	}
	var target string
	if err := s.db.QueryRowContext(ctx, probe, name).Scan(&target); err != nil {
		return err
	}
	if target != "" && target != s.schema {
		_, err := s.db.ExecContext(ctx, fmt.Sprintf("SET search_path TO %s, %s", quoteIdent(s.schema), quoteIdent(target)))
		return err
	}
	return nil
}

// quoteIdent quotes an SQL identifier. Identifiers come from trusted
// configuration, never from request input.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
