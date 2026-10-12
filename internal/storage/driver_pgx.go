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
	RegisterDriver(func(url string) (DB, error) {
		db, err := sql.Open("pgx", url)
		if err != nil {
			return nil, err
		}
		return &sqlDB{db: db}, nil
	})
}

// sqlDB adapts *sql.DB to the narrow DB interface.
type sqlDB struct{ db *sql.DB }

func (d *sqlDB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

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
