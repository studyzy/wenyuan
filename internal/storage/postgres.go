package storage

import (
	"context"
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
}

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
	for _, migration := range series {
		for _, statement := range migration.Statements() {
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration %04d failed: %w", migration.Version, err)
			}
		}
	}
	return nil
}

// quoteIdent quotes an SQL identifier. Identifiers come from trusted
// configuration, never from request input.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
