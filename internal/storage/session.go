package storage

import (
	"strconv"
	"time"
)

// SessionSettings are the settings a deployment applies to every database
// connection its pool opens.
//
// The reference runs the same list as a per-connection initialization callback,
// so a query that runs on a freshly created connection behaves the same as one
// on a long-lived connection. Applying them as connection parameters is the
// database/sql equivalent of that callback: a parameter is set when the
// connection is established, exactly once per connection.
type SessionSettings struct {
	// StatementTimeout bounds one statement server-side. Zero leaves the
	// server's own setting in place.
	StatementTimeout time.Duration
	// MaxParallelWorkersGather caps the planner's parallel workers per gather
	// node; nil leaves the server's own setting in place.
	MaxParallelWorkersGather *int
	// TrgmSimilarityThreshold is the pg_trgm similarity floor the `%` operator
	// probes at. Zero leaves the server's own setting in place.
	TrgmSimilarityThreshold float64
}

// RuntimeParams renders the settings as the connection parameters the driver
// applies when it opens a connection.
func (s SessionSettings) RuntimeParams() map[string]string {
	params := map[string]string{}
	if s.StatementTimeout > 0 {
		params["statement_timeout"] = formatPostgresMilliseconds(s.StatementTimeout)
	}
	if s.MaxParallelWorkersGather != nil {
		params["max_parallel_workers_per_gather"] = strconv.Itoa(*s.MaxParallelWorkersGather)
	}
	if s.TrgmSimilarityThreshold > 0 {
		params["pg_trgm.similarity_threshold"] = strconv.FormatFloat(s.TrgmSimilarityThreshold, 'f', -1, 64)
	}
	return params
}

// formatPostgresMilliseconds renders a duration in the unit Postgres accepts
// for statement_timeout, so a sub-second value is not rounded to zero seconds.
func formatPostgresMilliseconds(value time.Duration) string {
	return strconv.FormatInt(value.Milliseconds(), 10) + "ms"
}
