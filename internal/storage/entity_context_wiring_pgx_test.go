//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
)

// The PostgreSQL backend grades a mention the same way the in-memory one does:
// a shared neighbour carries a name-only near miss over the bar, a hub's
// damped vote does not, and a mention with no context scores the name alone
// even though the bank holds co-occurrence evidence. The items are timestamped
// as "none", so the recency share stays out of every score here and the
// co-occurrence signals decide alone.
func TestPostgresRetainEntityContextWiring(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hub        bool
		withNearby bool
		wantAlice  bool
	}{
		{name: "selective neighbour merges the mention", withNearby: true, wantAlice: false},
		{name: "hub neighbour leaves the mention apart", withNearby: true, hub: true, wantAlice: true},
		{name: "no neighbour scores the name alone", wantAlice: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := postgresVectorStore(t)
			ctx := context.Background()
			const bankID = "ctx-bank"
			if _, err := store.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
				t.Fatal(err)
			}
			seeded := make(map[string]Entity, 4)
			for _, name := range []string{"Alicia", "Berlin", "Carol", "Dave"} {
				entity, err := store.ResolveEntity(ctx, bankID, name, EntityKindRegular, false)
				if err != nil || entity.ID == "" {
					t.Fatalf("seed entity %q: %v (id %q)", name, err, entity.ID)
				}
				seeded[name] = entity
			}
			if err := store.UpsertUnitCooccurrences(ctx, bankID, []string{seeded["Alicia"].ID, seeded["Berlin"].ID}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if tc.hub {
				for _, partner := range []string{"Carol", "Dave"} {
					if err := store.UpsertUnitCooccurrences(ctx, bankID, []string{seeded["Berlin"].ID, seeded[partner].ID}, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
			}
			entities := []RetainEntity{{Text: "Alice"}}
			if tc.withNearby {
				entities = append(entities, RetainEntity{Text: "Berlin"})
			}
			if _, err := store.RetainWrite(ctx, RetainRequest{
				BankID: bankID,
				Items: []RetainItem{{
					Content:         "Alice met Berlin.",
					ResolveEntities: true,
					HasTimestamp:    true,
					Entities:        entities,
				}},
			}); err != nil {
				t.Fatal(err)
			}
			page, err := store.ListEntities(ctx, EntityQuery{BankID: bankID, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			created := false
			for _, entity := range page.Entities {
				if entity.CanonicalName == "Alice" {
					created = true
				}
			}
			if created != tc.wantAlice {
				t.Fatalf("an entity named %q exists = %v, want %v", "Alice", created, tc.wantAlice)
			}
		})
	}
}
