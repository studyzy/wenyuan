package storage

import "testing"

// The two rewrites the reference performs, and the cases it must leave alone.
func TestNormalizeDatabaseURL(t *testing.T) {
	cases := map[string]string{
		// Scheme translation.
		"postgresql+asyncpg://u:p@h:5432/db": "postgresql://u:p@h:5432/db",
		"postgres+asyncpg://h/db":            "postgresql://h/db",
		"postgres://h/db":                    "postgresql://h/db",
		// The asyncpg dialect's ssl parameter becomes libpq's.
		"postgresql+asyncpg://h/db?ssl=require": "postgresql://h/db?sslmode=require",
		"postgresql://h/db?ssl=verify-full":     "postgresql://h/db?sslmode=verify-full",
		// Other parameters survive, in order.
		"postgresql+asyncpg://h/db?application_name=w&ssl=require": "postgresql://h/db?application_name=w&sslmode=require",
		// Already normalized, or not PostgreSQL at all: unchanged.
		"postgresql://h/db?sslmode=require": "postgresql://h/db?sslmode=require",
		"postgresql://h/db":                 "postgresql://h/db",
		"mysql://h/db":                      "mysql://h/db",
		"sqlite://file.db":                  "sqlite://file.db",
		"pg0":                               "pg0",
		"":                                  "",
	}
	for input, want := range cases {
		if got := NormalizeDatabaseURL(input); got != want {
			t.Errorf("NormalizeDatabaseURL(%q) = %q, want %q", input, got, want)
		}
	}
	// Idempotent: normalizing twice changes nothing further.
	once := NormalizeDatabaseURL("postgresql+asyncpg://h/db?ssl=require")
	if twice := NormalizeDatabaseURL(once); twice != once {
		t.Fatalf("not idempotent: %q then %q", once, twice)
	}
}

// A non-PostgreSQL URL is never rewritten, and an unparseable one is handed on
// so the driver's own error reaches the operator.
func TestNormalizeDatabaseURLLeavesForeignInputAlone(t *testing.T) {
	for _, input := range []string{"oracle://h/db", "postgresql", "://broken"} {
		if got := NormalizeDatabaseURL(input); got != input {
			t.Errorf("NormalizeDatabaseURL(%q) = %q, want it unchanged", input, got)
		}
	}
}

// The pool budget reaches the driver registration, so a limit is in force from
// the first connection rather than after a burst.
func TestPoolBudgetReachesTheDriver(t *testing.T) {
	original := openDB
	defer func() { openDB = original }()
	var got PoolBudget
	RegisterDriver(func(_ string, budget PoolBudget) (DB, error) {
		got = budget
		return nil, ErrDriverUnavailable
	})
	_, _ = OpenDatabaseWithBudget("postgresql://h/db", PoolBudget{MaxOpenConns: 7, MaxIdleConns: 3})
	if got.MaxOpenConns != 7 || got.MaxIdleConns != 3 {
		t.Fatalf("driver received %+v", got)
	}
	// The plain entry point asks for no budget, which leaves the driver's own
	// default rather than imposing one.
	_, _ = OpenDatabase("postgresql://h/db")
	if got.MaxOpenConns != 0 || got.MaxIdleConns != 0 {
		t.Fatalf("the budget-less entry point passed %+v", got)
	}
}
