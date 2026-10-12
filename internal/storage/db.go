package storage

import (
	"context"
	"database/sql"
)

// DB is the narrow database contract the PostgreSQL store depends on. Keeping
// the surface small lets the store be driven by any SQL implementation
// (including a test double) and keeps the driver import isolated to one file.
type DB interface {
	// Ping verifies connectivity.
	Ping(ctx context.Context) error
	// ExecContext runs a statement that returns no rows.
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// QueryContext runs a statement returning rows.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	// QueryRowContext runs a statement returning at most one row.
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	// Close releases the underlying connection pool.
	Close() error
}

// OpenDBFunc constructs a DB from a data source URL. The pgx-backed
// implementation is registered here by a driver-specific file so this package
// builds with the standard library alone.
type OpenDBFunc func(url string) (DB, error)

var openDB OpenDBFunc = func(url string) (DB, error) {
	return nil, ErrDriverUnavailable
}

// RegisterDriver installs the database constructor used by the PostgreSQL
// backend. Callers that want a live PostgreSQL store import the driver file
// (behind its build tag) and register its constructor here.
func RegisterDriver(fn OpenDBFunc) {
	if fn != nil {
		openDB = fn
	}
}

// DriverAvailable reports whether a database constructor has been registered.
func DriverAvailable() bool { return openDB != nil }

// NewPostgresStore wraps an already-constructed DB in the PostgreSQL store.
func NewPostgresStore(db DB, schema string) *PostgresStore {
	return &PostgresStore{db: db, schema: schema}
}
