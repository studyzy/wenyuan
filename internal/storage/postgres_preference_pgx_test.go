//go:build pgx

package storage

import (
	"context"
	"testing"
)

// The in-memory cases pin that withFallback honours a read preference, but the
// PostgreSQL store copies the preference out of its own query type at two call
// sites (postgres_recall.go, postgres_memories.go). A field dropped there would
// still compile and still pass every in-process test, so these run the reads
// against a real database.
//
// The fixture's word is "deploy" alone: the native BM25 arm reduces a query to
// its most selective terms, so one term shared by both facts keeps either arm
// from narrowing the match set to the wrong bank.
func TestPostgresRecallCarriesTheFallbackPreference(t *testing.T) {
	pg, schema := seedPostgresPreferenceFixture(t)
	result, err := pg.Recall(context.Background(), RecallQuery{
		BankID:             "alice::Web",
		Query:              "deploy",
		Fallback:           &FallbackScope{Schema: schema, BankID: "acme::Web"},
		FallbackPreference: PreferPersonal,
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Text != "the deploy uses port 15432" {
		t.Fatalf("Recall matches = %v, want only the caller's own fact", result.Matches)
	}
}

func TestPostgresListMemoriesCarriesTheFallbackPreference(t *testing.T) {
	pg, schema := seedPostgresPreferenceFixture(t)
	page, err := pg.ListMemories(context.Background(), MemoryQuery{
		BankID:             "alice::Web",
		Fallback:           &FallbackScope{Schema: schema, BankID: "acme::Web"},
		FallbackPreference: PreferPersonal,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(page.Memories) != 1 || page.Memories[0].Text != "the deploy uses port 15432" {
		t.Fatalf("listing = %v, want only the caller's own fact", page.Memories)
	}
}

// seedPostgresPreferenceFixture puts one keyword-matchable fact in the caller's
// bank and one in the team bank, both inside the store's own schema, so the
// fallback scope names that schema too.
func seedPostgresPreferenceFixture(t *testing.T) (*PostgresStore, string) {
	t.Helper()
	pg := openMigratedStore(t, "pref")
	ctx := context.Background()
	for _, bankID := range []string{"alice::Web", "acme::Web"} {
		if _, err := pg.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
			t.Fatalf("create bank %s: %v", bankID, err)
		}
	}
	units := []MemoryUnit{
		{ID: pgUUID("pref-personal"), BankID: "alice::Web", Text: "the deploy uses port 15432", FactType: TypeWorld},
		{ID: pgUUID("pref-team"), BankID: "acme::Web", Text: "the team deploy uses port 15432", FactType: TypeWorld},
	}
	for _, unit := range units {
		if _, err := pg.UpsertUnit(ctx, unit); err != nil {
			t.Fatalf("upsert unit %s: %v", unit.ID, err)
		}
	}
	return pg, pg.defaultSchema
}
