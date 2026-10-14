//go:build pgx

package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// test-archive-fields-persist: the archive import round trip is lossless for
// the fact identity, observation scopes and consolidation timestamps that the
// importer used to drop. Before migration 0016 these had no column, so an
// export/import cycle silently discarded them.
func TestArchiveFactFieldsRoundTrip(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	consolidated := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	failed := time.Date(2026, 2, 4, 4, 5, 6, 0, time.UTC)
	unit := MemoryUnit{
		ID:                    pgUUID("roundtrip-unit"),
		BankID:                "vec-bank",
		Text:                  "the archive carried more than the text",
		FactType:              TypeExperience,
		SourceMemoryIDs:       []string{"11111111-1111-4111-8111-111111111111"},
		ObservationScopes:     json.RawMessage(`[["scope-a","scope-b"]]`),
		ConsolidatedAt:        &consolidated,
		ConsolidationFailedAt: &failed,
	}
	if _, err := store.UpsertUnit(ctx, unit); err != nil {
		t.Fatalf("upsert unit: %v", err)
	}

	var (
		sourceRaw   []byte
		scopes      string
		consolidate *time.Time
		failedAt    *time.Time
	)
	query := `SELECT source_memory_ids, observation_scopes::text, consolidated_at, consolidation_failed_at
	          FROM ` + quoteIdent(store.(*PostgresStore).schema) + `.memory_units WHERE id = $1`
	if err := store.(*PostgresStore).db.QueryRowContext(ctx, query, pgUUID("roundtrip-unit")).
		Scan(&sourceRaw, &scopes, &consolidate, &failedAt); err != nil {
		t.Fatalf("read back: %v", err)
	}
	sourceIDs := decodePostgresTextArray(sourceRaw)
	if len(sourceIDs) != 1 || sourceIDs[0] != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("source_memory_ids = %v, want the one archived id", sourceIDs)
	}
	if scopes != `[["scope-a", "scope-b"]]` {
		t.Errorf("observation_scopes = %s, want [[\"scope-a\", \"scope-b\"]]", scopes)
	}
	if consolidate == nil || !consolidate.Equal(consolidated) {
		t.Errorf("consolidated_at = %v, want %v", consolidate, consolidated)
	}
	if failedAt == nil || !failedAt.Equal(failed) {
		t.Errorf("consolidation_failed_at = %v, want %v", failedAt, failed)
	}
}
