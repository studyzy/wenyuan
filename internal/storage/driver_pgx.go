//go:build pgx

package storage

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// init registers the pgx-backed constructor when this file is built in. The
// default build omits it so the module compiles with the standard library
// alone; build with `-tags pgx` to enable live PostgreSQL access.
func init() {
	RegisterDriver(func(url string, budget PoolBudget) (DB, error) {
		db, err := openWithSession(url, budget)
		if err != nil {
			return nil, err
		}
		// The budget is applied before the pool is used, so a limit is in force
		// from the first statement rather than after a first burst.
		if budget.MaxOpenConns > 0 {
			db.SetMaxOpenConns(budget.MaxOpenConns)
		}
		if budget.MaxIdleConns > 0 {
			db.SetMaxIdleConns(budget.MaxIdleConns)
		}
		return newSQLDB(db, budget), nil
	})
}

// openWithSession opens the pool, carrying the deployment's session settings as
// connection parameters so every connection the pool creates has them. A
// deployment that configured none keeps the plain constructor.
func openWithSession(url string, budget PoolBudget) (*sql.DB, error) {
	params := budget.Session.RuntimeParams()
	if len(params) == 0 {
		return sql.Open("pgx", url)
	}
	config, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = map[string]string{}
	}
	for name, value := range params {
		config.RuntimeParams[name] = value
	}
	return stdlib.OpenDB(*config), nil
}

// connTx is a transaction bound to the connection the acquire observer took, so
// finishing the transaction returns that connection to the pool. The reference
// holds a pool connection for the life of a transaction the same way.
type connTx struct {
	tx   *sql.Tx
	conn connHandle
}

func (t *connTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

func (t *connTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

func (t *connTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, query, args...)
}

func (t *connTx) Commit() error {
	err := t.tx.Commit()
	_ = t.conn.Close()
	return err
}

func (t *connTx) Rollback() error {
	err := t.tx.Rollback()
	_ = t.conn.Close()
	return err
}

// sqlDB adapts *sql.DB to the narrow DB interface. Every statement it runs
// first waits for a pooled connection through the acquire observer, so a
// saturated pool is bounded and made visible rather than silently stalling.
type sqlDB struct {
	db      *sql.DB
	acquire acquireObserver
}

func newSQLDB(db *sql.DB, budget PoolBudget) *sqlDB {
	handle := &sqlDB{db: db}
	handle.acquire = acquireObserver{
		timeout: budget.AcquireTimeout,
		slow:    budget.PoolSlowAcquireThreshold,
		warn:    budget.AcquireWarnThreshold,
		stats:   handle.PoolStats,
	}
	return handle
}

func (d *sqlDB) Ping(ctx context.Context) error {
	conn, err := d.acquire.acquire(ctx, d.open)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.PingContext(ctx)
}

// BeginTx starts a transaction over a checked-out connection, so the
// transaction holds that connection until it commits or rolls back.
func (d *sqlDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (Tx, error) {
	conn, err := d.acquire.acquire(ctx, d.open)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &connTx{tx: tx, conn: conn}, nil
}

func (d *sqlDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	conn, err := d.acquire.acquire(ctx, d.open)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	return conn.ExecContext(ctx, query, args...)
}

func (d *sqlDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	conn, err := d.acquire.acquire(ctx, d.open)
	if err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// The caller keeps reading the rows after this returns. Closing the
	// connection blocks until those reads finish, so the release runs
	// alongside the scan instead of before it.
	releaseConn(conn)
	return rows, nil
}

func (d *sqlDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	conn, err := d.acquire.acquire(ctx, d.open)
	if err != nil {
		return errorRow(ctx, err)
	}
	row := conn.QueryRowContext(ctx, query, args...)
	// The row is consumed lazily, exactly like rows; release on the same terms.
	releaseConn(conn)
	return row
}

func (d *sqlDB) Close() error { return d.db.Close() }

// open takes one connection from the pool, blocking until one is free.
func (d *sqlDB) open(ctx context.Context) (connHandle, error) {
	return d.db.Conn(ctx)
}

// releaseConn returns a connection once the statement that holds it finishes.
// Closing a *sql.Conn blocks until the statement's rows are closed, so it runs
// on its own goroutine: the caller owns the rows' lifetime, and the connection
// goes back to the pool the moment the caller is done with them.
func releaseConn(conn connHandle) {
	go func() { _ = conn.Close() }()
}

// RawConn checks one connection out of the pool for direct driver access. The
// admin binary-COPY restore uses it to hand raw COPY streams to the server.
// The connection is returned to the pool when the caller closes it.
func (d *sqlDB) RawConn(ctx context.Context) (*sql.Conn, error) {
	return d.db.Conn(ctx)
}

// PoolStats reports the pool's current utilization for the readiness payload.
// The standard library exposes in-use, idle and maximum connections; it has no
// analogue for the reference's current-waiters gauge, so that key is left
// absent rather than reported as a misleading zero.
func (d *sqlDB) PoolStats() PoolStats {
	stats := d.db.Stats()
	return PoolStats{InUse: stats.InUse, Max: stats.MaxOpenConnections, Idle: stats.Idle}
}
