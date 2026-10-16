package storage

import (
	"testing"
	"time"
)

// The session settings render as the connection parameters the driver applies,
// with sub-second values kept in milliseconds.
func TestSessionSettingsRuntimeParams(t *testing.T) {
	parallel := 0
	params := SessionSettings{
		StatementTimeout:         600 * time.Second,
		MaxParallelWorkersGather: &parallel,
		TrgmSimilarityThreshold:  0.3,
	}.RuntimeParams()

	if params["statement_timeout"] != "600000ms" {
		t.Errorf("statement_timeout = %q, want 600000ms", params["statement_timeout"])
	}
	if params["max_parallel_workers_per_gather"] != "0" {
		t.Errorf("max_parallel_workers_per_gather = %q, want 0", params["max_parallel_workers_per_gather"])
	}
	if params["pg_trgm.similarity_threshold"] != "0.3" {
		t.Errorf("pg_trgm.similarity_threshold = %q, want 0.3", params["pg_trgm.similarity_threshold"])
	}

	if _, ok := (SessionSettings{}).RuntimeParams()["statement_timeout"]; ok {
		t.Fatal("an unset timeout must leave the server's own setting in place")
	}
}
