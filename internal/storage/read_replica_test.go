package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// handleDB is a DB double that records the statements one handle is asked to
// run and how often it was closed. Every statement fails with errStubRows (see
// bm25_terms_test.go), so a caller exercises only the routing, never the
// scanning.
type handleDB struct {
	queries []string
	execs   []string
	closes  int
}

func (d *handleDB) Ping(context.Context) error { return nil }

func (d *handleDB) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	d.execs = append(d.execs, query)
	return nil, errStubRows
}

func (d *handleDB) QueryContext(_ context.Context, query string, _ ...any) (*sql.Rows, error) {
	d.queries = append(d.queries, query)
	return nil, errStubRows
}

func (d *handleDB) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

func (d *handleDB) BeginTx(context.Context, *sql.TxOptions) (Tx, error) {
	return nil, errStubRows
}

func (d *handleDB) Close() error {
	d.closes++
	return nil
}

// A configured read replica serves the read paths; the write paths never touch
// it. The count per handle is the proof: ListTags issues exactly one query and
// DeleteBank exactly one statement, and each landed on its own handle.
func TestReadReplicaServesReadsAndNotWrites(t *testing.T) {
	ctx := context.Background()
	primary, replica := &handleDB{}, &handleDB{}
	store := NewPostgresStore(primary, "public")
	store.readDB = replica

	if _, err := store.ListTags(ctx, "bank"); !errors.Is(err, errStubRows) {
		t.Fatalf("ListTags error = %v, want the stub's", err)
	}
	if len(replica.queries) != 1 {
		t.Fatalf("the read replica ran %d queries, want 1", len(replica.queries))
	}
	if !strings.Contains(replica.queries[0], "memory_units") {
		t.Fatalf("the replica's query = %q, want the tags query", replica.queries[0])
	}
	if len(primary.queries) != 0 {
		t.Fatalf("the primary ran %d read queries, want 0: %v", len(primary.queries), primary.queries)
	}

	if _, err := store.DeleteBank(ctx, "bank"); !errors.Is(err, errStubRows) {
		t.Fatalf("DeleteBank error = %v, want the stub's", err)
	}
	if len(primary.execs) == 0 {
		t.Fatal("the primary ran no writes for DeleteBank")
	}
	if len(primary.queries) != 0 {
		t.Fatalf("DeleteBank read on the primary: %v", primary.queries)
	}
	if len(replica.execs) != 0 {
		t.Fatalf("the read replica ran %d writes, want 0: %v", len(replica.execs), replica.execs)
	}
}

// Without a read replica every statement — read and write — runs on the
// primary, which is the reference's behaviour when no read database is set.
func TestNoReadReplicaKeepsReadsOnPrimary(t *testing.T) {
	ctx := context.Background()
	primary := &handleDB{}
	store := NewPostgresStore(primary, "public")

	if store.reader() != primary {
		t.Fatal("reader() must be the primary when no read replica is configured")
	}
	if _, err := store.ListTags(ctx, "bank"); !errors.Is(err, errStubRows) {
		t.Fatalf("ListTags error = %v, want the stub's", err)
	}
	if len(primary.queries) != 1 {
		t.Fatalf("the primary ran %d queries, want 1", len(primary.queries))
	}
}

// Close releases the read pool with the store, so a process that shuts down
// does not leak the second handle.
func TestCloseReleasesReadPool(t *testing.T) {
	primary, replica := &handleDB{}, &handleDB{}
	store := NewPostgresStore(primary, "public")
	store.readDB = replica

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if primary.closes != 1 || replica.closes != 1 {
		t.Fatalf("closes = primary %d, replica %d; want 1 and 1", primary.closes, replica.closes)
	}
}

// --- Open-level wiring ---------------------------------------------------

const stubDriverName = "wenyuan_storage_readpool_test_stub"

var (
	stubDriverOnce sync.Once
	stubDriverMu   sync.Mutex
)

// stubDriver is a database/sql driver that answers every statement with one
// "on" row, so the store's Open (which reads standard_conforming_strings) can
// complete without a server. It records the DSN of every connection it opens,
// which is how the test sees that a second pool was really opened.
type stubDriver struct{}

func (stubDriver) Open(dsn string) (driver.Conn, error) {
	stubDriverMu.Lock()
	stubDriverDSNs = append(stubDriverDSNs, dsn)
	stubDriverMu.Unlock()
	return stubConn{}, nil
}

var stubDriverDSNs []string

type stubConn struct{}

func (stubConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (stubConn) Close() error                        { return nil }
func (stubConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }
func (stubConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &stubRows{}, nil
}

// sqlHandle adapts a *sql.DB to the package's DB contract, whose transaction
// and ping shapes differ from database/sql's.
type sqlHandle struct{ *sql.DB }

func (h sqlHandle) Ping(ctx context.Context) error { return h.PingContext(ctx) }

func (h sqlHandle) BeginTx(ctx context.Context, opts *sql.TxOptions) (Tx, error) {
	tx, err := h.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

type stubRows struct{ read bool }

func (*stubRows) Columns() []string { return []string{"setting"} }
func (*stubRows) Close() error      { return nil }
func (r *stubRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	dest[0] = "on"
	return nil
}

// openedHandle is one handle the replaced driver factory constructed, with the
// URL and budget the store asked for.
type openedHandle struct {
	url    string
	budget PoolBudget
	db     *sql.DB
}

// withStubDriver replaces the package's driver factory with one that builds a
// handle over the stub driver, recording each (URL, budget) pair. It restores
// the previous factory on cleanup.
func withStubDriver(t *testing.T) *[]openedHandle {
	t.Helper()
	stubDriverOnce.Do(func() { sql.Register(stubDriverName, stubDriver{}) })
	stubDriverMu.Lock()
	stubDriverDSNs = nil
	stubDriverMu.Unlock()

	opened := &[]openedHandle{}
	previous := openDB
	t.Cleanup(func() { openDB = previous })
	openDB = func(url string, budget PoolBudget) (DB, error) {
		handle, err := sql.Open(stubDriverName, url)
		if err != nil {
			return nil, err
		}
		*opened = append(*opened, openedHandle{url: url, budget: budget, db: handle})
		return sqlHandle{handle}, nil
	}
	return opened
}

// A configured read URL opens a second pool with the read pool's budget, and
// the store serves reads over it. Closing the store closes both pools.
func TestOpenBuildsReadPoolFromConfig(t *testing.T) {
	opened := withStubDriver(t)
	store, err := (&PostgresBackend{}).Open(context.Background(), BackendConfig{
		DatabaseURL:     "postgres://primary/db",
		ReadDatabaseURL: "postgres://replica/db",
		ReadPoolMin:     2,
		ReadPoolMax:     7,
		Session:         SessionSettings{StatementTimeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*opened) != 2 {
		t.Fatalf("opened %d handles, want 2: %+v", len(*opened), *opened)
	}
	if !strings.Contains((*opened)[0].url, "primary") {
		t.Fatalf("first handle url = %q, want the primary", (*opened)[0].url)
	}
	read := (*opened)[1]
	if !strings.Contains(read.url, "replica") {
		t.Fatalf("second handle url = %q, want the replica", read.url)
	}
	if read.budget.MaxOpenConns != 7 || read.budget.MaxIdleConns != 2 {
		t.Fatalf("read pool budget = %+v, want max 7 idle 2", read.budget)
	}
	if read.budget.Session.StatementTimeout != 5*time.Second {
		t.Fatalf("read pool statement timeout = %v, want 5s", read.budget.Session.StatementTimeout)
	}
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("store type = %T", store)
	}
	if pg.readDB == nil {
		t.Fatal("the store carries no read handle")
	}
	if pg.reader() != pg.readDB {
		t.Fatal("reader() must be the read handle when one is configured")
	}

	if err := pg.Close(); err != nil {
		t.Fatal(err)
	}
	for _, handle := range *opened {
		if err := handle.db.PingContext(context.Background()); err == nil {
			t.Fatalf("handle %q still answers after Close", handle.url)
		}
	}
	// Open pings the read handle once too, so the driver must have seen both
	// DSNs — a handle that was never connected is not a handle that works.
	stubDriverMu.Lock()
	dsns := append([]string(nil), stubDriverDSNs...)
	stubDriverMu.Unlock()
	sawReplica := false
	for _, dsn := range dsns {
		if strings.Contains(dsn, "replica") {
			sawReplica = true
		}
	}
	if !sawReplica {
		t.Fatalf("the read DSN was never connected: %v", dsns)
	}
}

// With no read URL there is no second pool: one handle, no read handle, and
// every statement goes to the primary.
func TestOpenWithoutReadURLOpensOnePool(t *testing.T) {
	opened := withStubDriver(t)
	store, err := (&PostgresBackend{}).Open(context.Background(), BackendConfig{
		DatabaseURL: "postgres://primary/db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*opened) != 1 {
		t.Fatalf("opened %d handles, want 1: %+v", len(*opened), *opened)
	}
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("store type = %T", store)
	}
	if pg.readDB != nil {
		t.Fatal("an unset read URL must not open a read handle")
	}
	if pg.reader() != pg.db {
		t.Fatal("without a read handle reader() must fall back to the primary")
	}
	_ = pg.Close()
}
