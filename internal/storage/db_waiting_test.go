package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"
)

// The waiter counter below is stdlib-only on purpose: it drives a real *sql.DB
// pool over a stub driver, so the counter is exercised with an actual pool but
// no live PostgreSQL and no pgx build tag.

// waitingConnector is a connector whose connections block on a channel the test
// controls, so a test can hold the pool's only connection and observe the
// second caller waiting for it.
type waitingConnector struct{ release chan struct{} }

func (c waitingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	select {
	case <-c.release:
		return &waitingConn{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (waitingConnector) Driver() driver.Driver { return waitingDriver{} }

type waitingDriver struct{}

func (waitingDriver) Open(string) (driver.Conn, error) { return &waitingConn{}, nil }

type waitingConn struct{}

func (*waitingConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (*waitingConn) Close() error                        { return nil }
func (*waitingConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (*waitingConn) Ping(context.Context) error          { return nil }

func (*waitingConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}

// The waiter counter is the analogue of the reference's pool instrumentation:
// it counts a caller from the moment it asks for a pooled connection until it
// has one, so a saturated pool is visible as a rising count and a completed
// acquire leaves none behind.
func TestPoolWaitingCountTracksBlockedAcquires(t *testing.T) {
	before := PoolWaitingCount()

	// The pool holds a single connection, created up front: the second caller
	// then waits in db.Conn rather than in the connector.
	release := make(chan struct{})
	close(release)
	pool := sql.OpenDB(waitingConnector{release: release})
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })

	held, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatalf("holding the pool's only connection: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		conn, err := acquirePooledConnection(context.Background(), func(ctx context.Context) (connHandle, error) {
			return pool.Conn(ctx)
		})
		if err == nil {
			err = conn.Close()
		}
		done <- err
	}()

	// The caller is blocked in db.Conn until the held connection goes back, so
	// it must be counted as waiting for that whole window.
	deadline := time.Now().Add(2 * time.Second)
	for PoolWaitingCount() <= before {
		if time.Now().After(deadline) {
			t.Fatalf("a blocked acquire was not counted: waiting=%d, before=%d", PoolWaitingCount(), before)
		}
		time.Sleep(time.Millisecond)
	}

	_ = held.Close()
	if err := <-done; err != nil {
		t.Fatalf("acquire error = %v", err)
	}
	if got := PoolWaitingCount(); got != before {
		t.Fatalf("waiting count after the acquire = %d, want %d", got, before)
	}
}

// A failed acquire must not leak a waiter: the reference decrements in a
// finally block for exactly this reason.
func TestPoolWaitingCountIsReleasedOnFailure(t *testing.T) {
	before := PoolWaitingCount()
	_, err := acquirePooledConnection(context.Background(), func(context.Context) (connHandle, error) {
		return nil, context.DeadlineExceeded
	})
	if err == nil {
		t.Fatal("a failed acquire must return its error")
	}
	if got := PoolWaitingCount(); got != before {
		t.Fatalf("waiting count after a failed acquire = %d, want %d", got, before)
	}
}

// The readiness payload's counter capability is satisfied by the pool's own
// handle, and it reports the counter's presence unconditionally: the reference
// writes db_pool_waiting whenever its instrumentation exists.
func TestPoolWaiterStatReportsTheCounter(t *testing.T) {
	waiting, ok := poolWaiterStat{}.PoolWaiting()
	if !ok {
		t.Fatal("the pool's waiter count is always reportable")
	}
	if waiting != PoolWaitingCount() {
		t.Fatalf("reported %d waiters, want %d", waiting, PoolWaitingCount())
	}
}
