//go:build pgx

package storage

import (
	"context"
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// init registers the pgx-backed constructor when this file is built in. The
// default build omits it so the module compiles with the standard library
// alone; build with `-tags pgx` to enable live PostgreSQL access.
func init() {
	RegisterDriver(func(url string, budget PoolBudget) (DB, error) {
		db, err := sql.Open("pgx", url)
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

// PoolStats reports the pool's current utilization for the readiness payload.
// The standard library exposes in-use, idle and maximum connections; it has no
// analogue for the reference's current-waiters gauge, so that key is left
// absent rather than reported as a misleading zero.
func (d *sqlDB) PoolStats() PoolStats {
	stats := d.db.Stats()
	return PoolStats{InUse: stats.InUse, Max: stats.MaxOpenConnections, Idle: stats.Idle}
}
