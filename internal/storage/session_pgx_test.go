//go:build pgx

package storage

import (
	"context"
	"os"
	"testing"
	"time"
)

// The session settings a deployment configures reach the server: they are
// carried as connection parameters, which the driver applies when it opens a
// connection, so a statement sees the same limits on a fresh connection as on a
// long-lived one.
func TestSessionSettingsReachTheServer(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the session-settings test was not run")
	}
	parallel := 0
	db, err := OpenDatabaseWithBudget(url, PoolBudget{Session: SessionSettings{
		StatementTimeout:         1234 * time.Millisecond,
		MaxParallelWorkersGather: &parallel,
		TrgmSimilarityThreshold:  0.42,
	}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	for setting, want := range map[string]string{
		"statement_timeout":               "1234ms",
		"max_parallel_workers_per_gather": "0",
		"pg_trgm.similarity_threshold":    "0.42",
	} {
		var got string
		if err := db.QueryRowContext(ctx, "SHOW "+setting).Scan(&got); err != nil {
			t.Fatalf("SHOW %s: %v", setting, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", setting, got, want)
		}
	}
}
