//go:build pgx

package storage

import (
	"context"
	"testing"
)

// The PostgreSQL backend resolves a near-matching entity name onto the stored
// one through the trigram similarity probe, which is the same primitive the
// reference applies. Without the trigram extension the probe is unavailable and
// the backend falls back to exact matching, so this test also proves the
// extension is installed and indexed by the migration.
func TestPostgresResolvesNearEntityNames(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	first, err := store.ResolveEntity(ctx, "vec-bank", "Acme Corporation", EntityKindRegular, true)
	if err != nil {
		t.Fatalf("resolve first: %v", err)
	}
	// A typo in the same name must resolve onto the stored entity.
	near, err := store.ResolveEntity(ctx, "vec-bank", "Acme Corporaton", EntityKindRegular, true)
	if err != nil {
		t.Fatalf("resolve near name: %v", err)
	}
	if near.ID != first.ID {
		t.Fatalf("a near name with resolution on must reuse the entity: %+v vs %+v", near, first)
	}

	// With resolution off the same near name creates its own entity.
	literal, err := store.ResolveEntity(ctx, "vec-bank", "Acme Corporaton", EntityKindRegular, false)
	if err != nil {
		t.Fatalf("resolve literal: %v", err)
	}
	if literal.ID == first.ID {
		t.Fatal("resolution off must not merge a non-exact name")
	}
}

// An exact name reuses the stored entity under either switch, so the switch
// only changes what counts as close.
func TestPostgresExactEntityMatchUnderBothModes(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	first, err := store.ResolveEntity(ctx, "vec-bank", "Globex", EntityKindRegular, false)
	if err != nil {
		t.Fatalf("resolve first: %v", err)
	}
	second, err := store.ResolveEntity(ctx, "vec-bank", "globex", EntityKindRegular, true)
	if err != nil {
		t.Fatalf("resolve case variant: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("a case-insensitive exact match must reuse the entity: %+v vs %+v", second, first)
	}
}

// A retain item's entities are resolved and linked to the units the write
// stored, so the graph connects the fact to the caller's entities.
func TestPostgresRetainLinksSuppliedEntities(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()

	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: "vec-bank",
		Items: []RetainItem{{
			Content:    "Alice works at Google on the AI team.",
			DocumentID: "doc-entity-1",
			Entities: []RetainEntity{
				{Text: "Alice", Kind: "PERSON"},
				{Text: "Google", Kind: "ORG"},
			},
		}},
	})
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	if len(outcome.ItemUnitIDs) != 1 || len(outcome.ItemUnitIDs[0]) == 0 {
		t.Fatalf("retain stored no units: %+v", outcome.ItemUnitIDs)
	}

	page, err := store.ListEntities(ctx, EntityQuery{BankID: "vec-bank"})
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	names := make(map[string]bool, len(page.Entities))
	for _, entity := range page.Entities {
		names[entity.Name] = true
	}
	for _, want := range []string{"Alice", "Google"} {
		if !names[want] {
			t.Fatalf("entity %q was not stored: %+v", want, names)
		}
	}
}
