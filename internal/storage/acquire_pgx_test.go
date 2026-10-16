//go:build pgx

package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// The tests below drive the sqlDB adapter over a stub SQL driver, so the
// acquire instrumentation is exercised with a real *sql.DB pool but no live
// PostgreSQL.

type fakeConnector struct{}

func (fakeConnector) Connect(context.Context) (driver.Conn, error) { return &fakeConn{}, nil }
func (fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{}, nil }

type fakeConn struct{}

func (*fakeConn) Prepare(string) (driver.Stmt, error)                          { return fakeStmt{}, nil }
func (*fakeConn) Close() error                                                 { return nil }
func (*fakeConn) Begin() (driver.Tx, error)                                    { return fakeTx{}, nil }
func (*fakeConn) Ping(context.Context) error                                   { return nil }
func (*fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) { return fakeTx{}, nil }

func (*fakeConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

func (*fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{}, nil
}

type fakeStmt struct{}

func (fakeStmt) Close() error  { return nil }
func (fakeStmt) NumInput() int { return 0 }
func (fakeStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (fakeStmt) Query([]driver.Value) (driver.Rows, error) { return &fakeRows{}, nil }

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeRows struct{}

func (*fakeRows) Columns() []string         { return []string{"n"} }
func (*fakeRows) Close() error              { return nil }
func (*fakeRows) Next([]driver.Value) error { return io.EOF }

func fakePool(t *testing.T) *sql.DB {
	t.Helper()
	pool := sql.OpenDB(fakeConnector{})
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func TestSQLDBAcquireExceedingTimeoutFails(t *testing.T) {
	pool := fakePool(t)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatalf("holding the only connection: %v", err)
	}
	defer func() { _ = held.Close() }()

	handle := newSQLDB(pool, PoolBudget{AcquireTimeout: 30 * time.Millisecond})
	_, err = handle.ExecContext(context.Background(), "SELECT 1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ExecContext error = %v, want a deadline-exceeded failure", err)
	}
}

func TestSQLDBAcquireBelowTimeoutSucceeds(t *testing.T) {
	pool := fakePool(t)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatalf("holding the only connection: %v", err)
	}

	handle := newSQLDB(pool, PoolBudget{AcquireTimeout: time.Second})
	done := make(chan error, 1)
	go func() {
		_, err := handle.ExecContext(context.Background(), "SELECT 1")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = held.Close()

	if err := <-done; err != nil {
		t.Fatalf("ExecContext error = %v, want success once the connection frees", err)
	}
}

func TestSQLDBSlowAcquireWarns(t *testing.T) {
	pool := fakePool(t)
	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatalf("holding the only connection: %v", err)
	}

	var mu sync.Mutex
	var waits []time.Duration
	handle := &sqlDB{db: pool}
	handle.acquire = acquireObserver{
		timeout: time.Second,
		warn:    10 * time.Millisecond,
		stats:   handle.PoolStats,
		emit:    func(wait time.Duration, _ PoolStats) { mu.Lock(); waits = append(waits, wait); mu.Unlock() },
	}

	done := make(chan error, 1)
	go func() {
		_, err := handle.ExecContext(context.Background(), "SELECT 1")
		done <- err
	}()
	time.Sleep(60 * time.Millisecond)
	_ = held.Close()
	if err := <-done; err != nil {
		t.Fatalf("ExecContext error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 1 {
		t.Fatalf("reported %d slow acquires, want 1", len(waits))
	}
	if waits[0] < 10*time.Millisecond {
		t.Fatalf("reported wait = %v, want at least the threshold", waits[0])
	}
}

func TestSQLDBQueryContextReturnsConnectionToPool(t *testing.T) {
	pool := fakePool(t)
	handle := newSQLDB(pool, PoolBudget{})

	rows, err := handle.QueryContext(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("QueryContext error = %v", err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error = %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("closing rows: %v", err)
	}

	// The connection goes back to the pool once the rows are closed; the
	// release runs alongside the scan, so give it a moment to finish.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := handle.ExecContext(context.Background(), "SELECT 1")
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("connection was not returned to the pool: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSQLDBBeginTxHoldsConnectionUntilDone(t *testing.T) {
	pool := fakePool(t)
	handle := newSQLDB(pool, PoolBudget{AcquireTimeout: 30 * time.Millisecond})

	tx, err := handle.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx error = %v", err)
	}
	if _, err := handle.ExecContext(context.Background(), "SELECT 1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ExecContext while the transaction holds the pool = %v, want a deadline-exceeded failure", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback error = %v", err)
	}
	if _, err := handle.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("ExecContext after the transaction ended = %v, want success", err)
	}
}
