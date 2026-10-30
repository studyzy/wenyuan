//go:build pgx

package storage

import (
	"context"
	"testing"
)

// The memories-ingested chart counts only the memories the reader's tag filter
// admits, the way the reference AND-s the caller's resolved scope into the
// bucketing query (engine/memories/pg/counts.py:138-148). A nil filter still
// counts the bank. Pinned on the SQL backend because that is where the scope
// becomes a predicate.
func TestPostgresMemoriesTimeseriesHonorsTagScope(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	const bank = "w15-timeseries-scope"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	for _, unit := range []MemoryUnit{
		{ID: newID(), BankID: bank, Text: "Kate is interviewing", FactType: TypeWorld, Tags: []string{"user:kate"}},
		{ID: newID(), BankID: bank, Text: "Dan deploys", FactType: TypeWorld, Tags: []string{"user:dan"}},
		{ID: newID(), BankID: bank, Text: "Nobody deploys on Fridays", FactType: TypeWorld, Tags: []string{"kind:rule"}},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatalf("upsert unit: %v", err)
		}
	}
	counted := func(scope []TagGroup) int {
		t.Helper()
		series, err := store.MemoriesTimeseries(ctx, bank, "7d", "created_at", scope)
		if err != nil {
			t.Fatalf("timeseries: %v", err)
		}
		total := 0
		for _, bucket := range series.Buckets {
			total += bucket.Count
		}
		return total
	}

	if got := counted(nil); got != 3 {
		t.Fatalf("unscoped counts %d memories, want the bank's 3", got)
	}
	dan := []TagGroup{{Tags: []string{"user:dan", "kind:rule"}, Match: TagsMatchAnyStrict}}
	if got := counted(dan); got != 2 {
		t.Fatalf("dan's scope counts %d memories, want his own and the shared rule", got)
	}
	kate := []TagGroup{{Tags: []string{"user:kate"}, Match: TagsMatchAnyStrict}}
	if got := counted(kate); got != 1 {
		t.Fatalf("kate's scope counts %d memories, want only her own", got)
	}
}
