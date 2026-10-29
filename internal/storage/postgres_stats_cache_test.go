//go:build pgx

package storage

import (
	"context"
	"testing"
)

// test-bank-stats-cache: a computed summary is written to the cross-process
// cache table and served from it on the next call, so a second worker reads it
// without re-running the aggregates.
func TestBankStatsCacheRoundTrip(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	stats, err := store.BankStats(ctx, "vec-bank")
	if err != nil {
		t.Fatalf("first stats: %v", err)
	}

	// The cache row now exists and holds the summary the call computed.
	var payload string
	query := `SELECT payload::text FROM ` + quoteIdent(store.(*PostgresStore).defaultSchema) + `.bank_stats_cache WHERE bank_id = $1`
	if err := store.(*PostgresStore).db.QueryRowContext(ctx, query, "vec-bank").Scan(&payload); err != nil {
		t.Fatalf("cache row missing: %v", err)
	}

	cached, err := store.BankStats(ctx, "vec-bank")
	if err != nil {
		t.Fatalf("cached stats: %v", err)
	}
	if cached.TotalNodes != stats.TotalNodes || cached.TotalDocuments != stats.TotalDocuments {
		t.Fatalf("cached stats = %+v, want %+v", cached, stats)
	}
}
