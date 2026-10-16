package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// observerConn satisfies connHandle; the observer tests never run a statement, so
// only Close is reached.
type observerConn struct{}

func (observerConn) Close() error                                                    { return nil }
func (observerConn) PingContext(context.Context) error                               { return nil }
func (observerConn) ExecContext(context.Context, string, ...any) (sql.Result, error) { return nil, nil }
func (observerConn) QueryContext(context.Context, string, ...any) (*sql.Rows, error) { return nil, nil }
func (observerConn) QueryRowContext(context.Context, string, ...any) *sql.Row        { return nil }
func (observerConn) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) {
	return nil, errStubNoTx
}

var errStubNoTx = errors.New("stub conn has no transactions")

// blockedOpen stands in for a saturated pool: it never hands over a connection
// until the context is done, exactly as db.Conn blocks while every connection
// is in use.
func blockedOpen(ctx context.Context) (connHandle, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAcquireFailsWhenWaitExceedsTimeout(t *testing.T) {
	observer := acquireObserver{timeout: 20 * time.Millisecond}
	_, err := observer.acquire(context.Background(), blockedOpen)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("acquire error = %v, want a deadline-exceeded failure", err)
	}
}

func TestAcquireSucceedsWhenWaitIsBelowTimeout(t *testing.T) {
	observer := acquireObserver{timeout: time.Second}
	conn, err := observer.acquire(context.Background(), func(ctx context.Context) (connHandle, error) {
		// A healthy pool returns a connection without waiting out the timeout.
		return observerConn{}, nil
	})
	if err != nil {
		t.Fatalf("acquire error = %v, want success", err)
	}
	if _, ok := conn.(observerConn); !ok {
		t.Fatalf("acquire returned %T, want the stub connection", conn)
	}
}

func TestAcquireWaitsBelowTimeoutBeforeSucceeding(t *testing.T) {
	observer := acquireObserver{timeout: time.Second}
	conn, err := observer.acquire(context.Background(), func(ctx context.Context) (connHandle, error) {
		select {
		case <-time.After(10 * time.Millisecond):
			return observerConn{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	if err != nil {
		t.Fatalf("acquire error = %v, want success after a short wait", err)
	}
	if _, ok := conn.(observerConn); !ok {
		t.Fatalf("acquire returned %T, want the stub connection", conn)
	}
}

// advancingClock returns a clock whose successive reads jump by step, so a
// measured wait is exact without sleeping.
func advancingClock(start time.Time, step time.Duration) func() time.Time {
	reads := 0
	return func() time.Time {
		now := start.Add(time.Duration(reads) * step)
		reads++
		return now
	}
}

func TestAcquireWarnsWhenWaitReachesThreshold(t *testing.T) {
	var got []time.Duration
	observer := acquireObserver{
		warn:  time.Second,
		now:   advancingClock(time.Unix(0, 0), 2*time.Second),
		emit:  func(wait time.Duration, _ PoolStats) { got = append(got, wait) },
		stats: func() PoolStats { return PoolStats{InUse: 3, Max: 3} },
	}
	if _, err := observer.acquire(context.Background(), func(context.Context) (connHandle, error) {
		return observerConn{}, nil
	}); err != nil {
		t.Fatalf("acquire error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("emitted %d slow acquires, want 1", len(got))
	}
	if got[0] != 2*time.Second {
		t.Fatalf("reported wait = %v, want 2s", got[0])
	}
}

func TestAcquireDoesNotWarnBelowThreshold(t *testing.T) {
	emitted := false
	observer := acquireObserver{
		warn: time.Second,
		now:  advancingClock(time.Unix(0, 0), 10*time.Millisecond),
		emit: func(time.Duration, PoolStats) { emitted = true },
	}
	if _, err := observer.acquire(context.Background(), func(context.Context) (connHandle, error) {
		return observerConn{}, nil
	}); err != nil {
		t.Fatalf("acquire error = %v", err)
	}
	if emitted {
		t.Fatal("a healthy acquire warned, want silence")
	}
}

func TestAcquireOffThresholdsNeverWarn(t *testing.T) {
	emitted := false
	observer := acquireObserver{
		// slow = 0 and warn = 0 disable both warnings, the shipped defaults.
		now:  advancingClock(time.Unix(0, 0), time.Hour),
		emit: func(time.Duration, PoolStats) { emitted = true },
	}
	if _, err := observer.acquire(context.Background(), func(context.Context) (connHandle, error) {
		return observerConn{}, nil
	}); err != nil {
		t.Fatalf("acquire error = %v", err)
	}
	if emitted {
		t.Fatal("a disabled threshold warned")
	}
}

func TestAcquireSlowThresholdIsExclusive(t *testing.T) {
	emitted := 0
	observer := acquireObserver{
		slow: 2 * time.Second,
		now:  advancingClock(time.Unix(0, 0), 2*time.Second),
		emit: func(time.Duration, PoolStats) { emitted++ },
	}
	if _, err := observer.acquire(context.Background(), func(context.Context) (connHandle, error) {
		return observerConn{}, nil
	}); err != nil {
		t.Fatalf("acquire error = %v", err)
	}
	if emitted != 0 {
		t.Fatalf("a wait equal to the slow threshold warned %d times, want 0", emitted)
	}
}

func TestErrorRowReportsTheAcquireError(t *testing.T) {
	sentinel := errors.New("pool saturated")
	var value int
	err := errorRow(context.Background(), sentinel).Scan(&value)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Scan error = %v, want the acquire error", err)
	}
}
