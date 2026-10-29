package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
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
	// Waiting is how many callers are currently blocked waiting for a pooled
	// connection, which is what distinguishes a saturated pool from a busy one.
	// A store that cannot count its waiters leaves it zero; the reference
	// reports the same key either way.
	Waiting int
}

// waitingAcquires counts the in-flight acquisitions that have not obtained a
// connection yet. database/sql exposes the pool's size and idle counts but not
// its queue depth, so the acquire path counts what is blocked in db.Conn: the
// strict analogue of the reference's pool instrumentation, which increments
// around pool.acquire() and never lets a failed acquire leak a waiter.
var waitingAcquires atomic.Int64

// PoolWaitingCount returns how many callers are currently waiting to acquire a
// pooled connection, process-wide. The reference reports the same counter in
// its readiness payload and its pool-waiting gauge whether or not the backend
// can report its pool utilization.
func PoolWaitingCount() int { return int(waitingAcquires.Load()) }

// acquirePooledConnection takes a pooled connection through open, counted as a
// waiter until it has one. Every acquisition this package makes goes through
// here, so the counter measures exactly what the reference's instrumentation
// wraps. A store that is not backed by a pool never calls it, which is what
// makes the counter's presence in the readiness payload mean "this backend has
// the instrumentation".
func acquirePooledConnection(ctx context.Context, open func(context.Context) (connHandle, error)) (connHandle, error) {
	waitingAcquires.Add(1)
	defer waitingAcquires.Add(-1)
	return open(ctx)
}

// PoolWaiter reports the process-wide pooled-acquire waiter count. It is the
// capability a store implements when it is backed by a counted pool: the
// readiness payload then carries db_pool_waiting whether or not the backend can
// also report its utilization, as the reference's instrumentation does.
type PoolWaiter interface {
	PoolWaiting() (int, bool)
}

// poolWaiterStat is the value PoolWaiter is satisfied with; the counter is
// process-wide, so any store that is backed by this package's pool reports the
// same number.
type poolWaiterStat struct{}

// PoolWaiting reports the current waiter count. The counter always exists once
// this package's pool is in use, so the report is never absent.
func (poolWaiterStat) PoolWaiting() (int, bool) { return PoolWaitingCount(), true }

// PoolStatter is the optional capability a store implements when it can report
// its connection-pool utilization. A store with no pool does not implement it,
// and its consumers then omit the pool keys rather than report zeros.
type PoolStatter interface {
	PoolStats() (PoolStats, bool)
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
	// Session holds the settings every connection of this pool carries.
	Session SessionSettings
	// AcquireTimeout bounds how long a statement waits for a pooled connection
	// before failing, so a saturated pool surfaces as an error instead of a
	// request that hangs. Zero leaves the wait unbounded, which is the driver's
	// own behaviour.
	AcquireTimeout time.Duration
	// PoolSlowAcquireThreshold, when above zero, declares a connection acquire
	// slow once it waits longer than it. Zero disables this warning.
	PoolSlowAcquireThreshold time.Duration
	// AcquireWarnThreshold, when above zero, warns once an acquire waits at least
	// this long, logging the pool's utilization so a saturated pool is visible
	// rather than only slow. Zero disables it.
	AcquireWarnThreshold time.Duration
}

// acquireObserver measures and bounds how long a statement waits for a pooled
// connection, and reports a wait that is long enough to mean the pool is
// saturated.
//
// database/sql exposes neither the pool's current waiter count nor a hook on a
// plain statement call, but it does let a caller take a connection explicitly:
// db.Conn blocks until one is free, so timing that call measures the wait while
// a deadline on its context bounds it. The observed wait is what the reference's
// pool-instrumentation module records.
type acquireObserver struct {
	// timeout bounds the wait for a connection. Zero leaves it unbounded.
	timeout time.Duration
	// slow and warn are the two thresholds at which a wait is reported; each is
	// disabled when zero. slow is the reference's pool-slow warning and warn its
	// acquire-warn warning; both mean "the pool made this statement wait".
	slow time.Duration
	warn time.Duration
	// now reads the clock; nil means time.Now.
	now func() time.Time
	// emit receives a reported acquire with the pool's utilization. Nil uses the
	// package logger.
	emit func(wait time.Duration, stats PoolStats)
	// stats reports the pool's utilization for the warning. Nil reports zeros.
	stats func() PoolStats
}

// acquire takes a connection through open, bounded by the acquire timeout, and
// reports the wait when it is slow. A failure to obtain a connection within the
// timeout is returned to the caller rather than waited out.
func (o acquireObserver) acquire(ctx context.Context, open func(context.Context) (connHandle, error)) (connHandle, error) {
	start := o.clock()
	acquireCtx := ctx
	if o.timeout > 0 {
		var cancel context.CancelFunc
		acquireCtx, cancel = context.WithTimeout(ctx, o.timeout)
		// The connection is already taken once open returns; releasing the
		// deadline's timer here does not take the connection back.
		defer cancel()
	}
	conn, err := acquirePooledConnection(acquireCtx, open)
	if err != nil {
		return nil, fmt.Errorf("waiting for a database connection: %w", err)
	}
	wait := o.clock().Sub(start)
	o.observe(wait)
	// The wait is also attributed to the retain in scope, if there is one, so a
	// pool wait appears in that retain's own breakdown beside its other phases
	// rather than only as a fleet-wide histogram. A no-op outside a retain.
	recordRetainAcquireWait(ctx, wait)
	return conn, nil
}

// observe reports a completed acquire when it waited at least one of the
// configured thresholds.
func (o acquireObserver) observe(wait time.Duration) {
	if !o.slowEnough(wait) {
		return
	}
	var stats PoolStats
	if o.stats != nil {
		stats = o.stats()
	}
	if o.emit != nil {
		o.emit(wait, stats)
		return
	}
	slog.Default().Warn("slow DB pool acquire: waited for a connection",
		"waited_seconds", wait.Seconds(),
		"in_use", stats.InUse, "max", stats.Max, "idle", stats.Idle)
}

func (o acquireObserver) slowEnough(wait time.Duration) bool {
	if o.slow > 0 && wait > o.slow {
		return true
	}
	return o.warn > 0 && wait >= o.warn
}

func (o acquireObserver) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

// connHandle is the connection-scoped half of database/sql that a statement
// runs on once a connection has been acquired. *sql.Conn satisfies it; keeping
// it an interface lets the acquire instrumentation be exercised without a live
// database.
type connHandle interface {
	Close() error
	PingContext(ctx context.Context) error
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// errorRow returns a row whose Scan reports err, so a QueryRowContext that
// failed before a statement ran surfaces that failure where the caller already
// reads it. database/sql exposes no way to build one, so it comes from a query
// against a handle whose connector refuses the connection: the error then
// travels the ordinary query path and is returned verbatim by Scan.
func errorRow(ctx context.Context, err error) *sql.Row {
	handle := sql.OpenDB(errorRowConnector{})
	row := handle.QueryRowContext(context.WithValue(ctx, errorRowKey{}, err), "")
	_ = handle.Close()
	return row
}

type errorRowKey struct{}

// errorRowConnector is a driver.Connector whose Connect always fails with the
// error carried on the context, so a database handle built from it yields
// exactly that error from any query.
type errorRowConnector struct{}

func (errorRowConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err, ok := ctx.Value(errorRowKey{}).(error); ok {
		return nil, err
	}
	return nil, errNoRowError
}

func (errorRowConnector) Driver() driver.Driver { return errorRowDriver{} }

// errorRowDriver is the connector's driver; Open is never reached because
// Connect fails first.
type errorRowDriver struct{}

func (errorRowDriver) Open(string) (driver.Conn, error) { return nil, errNoRowError }

var errNoRowError = errors.New("no error row")

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
	return &PostgresStore{db: db, defaultSchema: schema, tuning: DefaultTuning(), recallGate: newRecallGate(DefaultTuning().MaxConcurrent)}
}
