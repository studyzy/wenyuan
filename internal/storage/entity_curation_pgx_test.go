//go:build pgx

package storage

import (
	"context"
	"slices"
	"testing"
)

// Curation rewrites a unit's entity links against PostgreSQL too.
//
// The in-memory backend rewrote them from the start, so an entity edit through
// the API looked correct in the offline suite and did nothing at all in a
// deployment: the response was a 200 and the links were untouched.
func TestPostgresCurationReplacesUnitEntities(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	const bank = "curation-entities-bank"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: "Curation"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}

	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items: []RetainItem{{
			Content:  "Ada Lovelace wrote notes.",
			Entities: []RetainEntity{{Text: "Ada Lovelace"}},
		}},
	})
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	memoryID := outcome.ItemUnitIDs[0][0]

	names := []string{"Ada Lovelace", "Grace Hopper"}
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Entities: &names}); err != nil {
		t.Fatalf("update entities: %v", err)
	}
	got, err := store.UnitEntities(ctx, bank, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"Ada Lovelace", "Grace Hopper"}) {
		t.Fatalf("entities = %v, want both names", got)
	}

	// The bank's mention count and the entity list follow the links: the new
	// entity is discoverable and the dropped one stops being mentioned.
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Entities: &[]string{"Ada Lovelace"}}); err != nil {
		t.Fatalf("replace entities: %v", err)
	}
	got, err = store.UnitEntities(ctx, bank, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"Ada Lovelace"}) {
		t.Fatalf("entities after replace = %v, want only the kept name", got)
	}

	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Entities: &[]string{}}); err != nil {
		t.Fatalf("clear entities: %v", err)
	}
	got, err = store.UnitEntities(ctx, bank, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("entities after clearing = %v, want none", got)
	}
}
