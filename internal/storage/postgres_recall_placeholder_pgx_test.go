//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
)

// A placeholder date is no date in the PostgreSQL recall path either: a unit
// dated at year 1 or 9999 — an open bound, not a moment — ranks by the dates it
// really has, like the in-process backend (engine/stored_dates.py; the
// reference wraps the same reads at engine/memories/pg/recall.py:322-334).
func TestPostgresRecallPlaceholderDateRanksAsUnknown(t *testing.T) {
	store := postgresVectorStore(t)
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatalf("want *PostgresStore, got %T", store)
	}
	ctx := context.Background()
	mention := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	anchor := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)

	final := func(bank string, occurredStart *time.Time) float64 {
		t.Helper()
		if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
			t.Fatalf("create bank %s: %v", bank, err)
		}
		if occurredStart == nil {
			if _, err := pg.db.ExecContext(ctx,
				`INSERT INTO `+quoteIdent(pg.defaultSchema)+`.memory_units (bank_id, text, mentioned_at)
				 VALUES ($1, $2, $3)`, bank, "alpha", mention); err != nil {
				t.Fatalf("insert unit: %v", err)
			}
		} else {
			if _, err := pg.db.ExecContext(ctx,
				`INSERT INTO `+quoteIdent(pg.defaultSchema)+`.memory_units (bank_id, text, mentioned_at, occurred_start)
				 VALUES ($1, $2, $3, $4)`, bank, "alpha", mention, *occurredStart); err != nil {
				t.Fatalf("insert unit: %v", err)
			}
		}
		// An empty query with no embedding recalls through the newest-units arm,
		// so the ranking difference is the combined score's recency signal alone.
		result, err := store.Recall(ctx, RecallQuery{BankID: bank, QueryTimestamp: &anchor})
		if err != nil {
			t.Fatalf("recall %s: %v", bank, err)
		}
		if len(result.Matches) != 1 {
			t.Fatalf("bank %s: want one match, got %v", bank, result.Matches)
		}
		return result.Matches[0].Final
	}

	plain := final("w13-plain-date", nil)
	for _, placeholder := range []time.Time{
		time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC),
	} {
		stored := placeholder
		bank := "w13-placeholder-" + stored.Format("2006")
		got := final(bank, &stored)
		if got != plain {
			t.Fatalf("placeholder occurred_start %s must rank as unknown: got %v, want %v",
				stored.Format(time.RFC3339), got, plain)
		}
	}
}
