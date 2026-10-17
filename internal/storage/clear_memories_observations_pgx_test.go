//go:build pgx

package storage

import (
	"context"
	"errors"
	"testing"
)

// A type-scoped clear removes the observations derived from the units it drops:
// an observation whose evidence is gone would otherwise keep answering, still
// pointing at ids that no longer exist. The surviving co-sources are also left
// to be re-consolidated. Ledger 96 D2.
func TestPostgresClearByTypeDropsDependentObservations(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "clear_obs")
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	const (
		worldID = "bbbbbbbb-0000-0000-0000-000000000001"
		otherID = "bbbbbbbb-0000-0000-0000-000000000002"
		obsID   = "bbbbbbbb-0000-0000-0000-000000000003"
	)
	for _, unit := range []MemoryUnit{
		{ID: worldID, BankID: "b1", Text: "world fact", FactType: TypeWorld},
		{ID: otherID, BankID: "b1", Text: "other fact", FactType: TypeExperience},
		{ID: obsID, BankID: "b1", Text: "derived observation", FactType: TypeObservation,
			SourceMemoryIDs: []string{worldID, otherID}},
	} {
		if _, err := pg.UpsertUnit(ctx, unit); err != nil {
			t.Fatalf("upsert %s: %v", unit.ID, err)
		}
	}

	removed, err := pg.ClearBankMemories(ctx, "b1", string(TypeWorld))
	if err != nil {
		t.Fatalf("scoped clear: %v", err)
	}
	if removed != 1 {
		t.Fatalf("scoped clear removed %d, want 1", removed)
	}

	// The observation that rested on the removed world fact is gone.
	if _, err := pg.GetMemory(ctx, "b1", obsID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("dependent observation after clear = %v, want ErrMemoryNotFound", err)
	}
	// The world fact itself is gone, the unrelated one survives.
	if _, err := pg.GetMemory(ctx, "b1", worldID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("cleared world fact = %v, want ErrMemoryNotFound", err)
	}
	if _, err := pg.GetMemory(ctx, "b1", otherID); err != nil {
		t.Fatalf("unrelated fact must survive: %v", err)
	}
}
