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
	// BeginTx starts a transaction, so a multi-statement change is atomic
	// rather than partially applied when one statement fails.
	BeginTx(ctx context.Context, opts *sql.TxOptions) (Tx, error)
	// Close releases the underlying connection pool.
	Close() error
}

// Tx is the subset of transaction behaviour this area needs. It mirrors the
// statement methods of DB so a caller can write the same query either way.
type Tx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	Commit() error
	Rollback() error
}

// PoolStats is current database connection-pool utilization, reported for the
// readiness payload.
type PoolStats struct {
	InUse int
	Max   int
	Idle  int
}

// PoolBudget bounds the connection pool one store opens.
//
// An unbounded pool grows until the database refuses connections, which turns
// a load spike into an outage for every other client of that database. The
// budget is per store, because a deployment runs one store per process.
type PoolBudget struct {
	// MaxOpenConns bounds simultaneous connections. Zero leaves the driver's
	// own default.
	MaxOpenConns int
	// MaxIdleConns bounds connections kept open when unused. Zero leaves the
	// driver's own default.
	MaxIdleConns int
}

// OpenDBFunc constructs a DB from a data source URL and pool budget. The
// pgx-backed implementation is registered here by a driver-specific file so
// this package builds with the standard library alone.
type OpenDBFunc func(url string, budget PoolBudget) (DB, error)

var openDB OpenDBFunc = func(string, PoolBudget) (DB, error) {
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

// OpenDatabase constructs a database handle from a URL using the registered
// driver. It is the public entry for callers that need to read a foreign,
// reference-shaped database rather than open this project's own store.
func OpenDatabase(url string) (DB, error) {
	return openDB(url, PoolBudget{})
}

// OpenDatabaseWithBudget constructs a database handle with a connection budget.
func OpenDatabaseWithBudget(url string, budget PoolBudget) (DB, error) {
	return openDB(url, budget)
}

// NewPostgresStore wraps an already-constructed DB in the PostgreSQL store.
func NewPostgresStore(db DB, schema string) *PostgresStore {
	return &PostgresStore{db: db, schema: schema}
}
