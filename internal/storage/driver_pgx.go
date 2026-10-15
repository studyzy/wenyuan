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
		return &sqlDB{db: db}, nil
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

// sqlDB adapts *sql.DB to the narrow DB interface.
type sqlDB struct{ db *sql.DB }

func (d *sqlDB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// BeginTx starts a transaction over the pool.
func (d *sqlDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (Tx, error) {
	return d.db.BeginTx(ctx, opts)
}

func (d *sqlDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.db.ExecContext(ctx, query, args...)
}

func (d *sqlDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, query, args...)
}

func (d *sqlDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.db.QueryRowContext(ctx, query, args...)
}

func (d *sqlDB) Close() error { return d.db.Close() }

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
