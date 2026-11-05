package storage

import (
	"context"
	"testing"
	"time"
)

// wiringStore builds an in-memory store with the shipped merge floor, so the
// wiring tests grade candidates exactly as a deployment without tuning does.
func wiringStore(t *testing.T) *MemoryStore {
	t.Helper()
	store := NewMemoryStore("public")
	store.SetTuning(Tuning{EntityMergeMinSimilarity: DefaultEntityMergeMinSimilarity})
	return store
}

// seedWiringEntities creates the named entities literally, the way a caller
// with resolution off does, so a test seeds the registry without scoring.
func seedWiringEntities(t *testing.T, ctx context.Context, store *MemoryStore, bankID string, names ...string) map[string]Entity {
	t.Helper()
	seeded := make(map[string]Entity, len(names))
	for _, name := range names {
		entity, err := store.ResolveEntity(ctx, bankID, name, EntityKindRegular, false)
		if err != nil || entity.ID == "" {
			t.Fatalf("seed entity %q: %v (id %q)", name, err, entity.ID)
		}
		seeded[name] = entity
	}
	return seeded
}

// entityByName returns the bank's stored entity for a canonical name.
func entityByName(store *MemoryStore, bankID, name string) (Entity, bool) {
	for _, stored := range store.entities {
		if stored.BankID == bankID && stored.CanonicalName == name {
			return stored, true
		}
	}
	return Entity{}, false
}

// A nearby name the candidate was seen beside carries a name-only near miss
// over the merge bar: the mention of "Alice" beside "Berlin" lands on the
// stored "Alicia", which the name alone never justifies.
func TestRetainNearbyCooccurrenceCarriesCandidateOverTheBar(t *testing.T) {
	store := wiringStore(t)
	ctx := context.Background()
	const bankID = "wiring-cooc"
	if _, err := store.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
		t.Fatal(err)
	}
	seeded := seedWiringEntities(t, ctx, store, bankID, "Alicia", "Berlin")
	if err := store.UpsertUnitCooccurrences(ctx, bankID, []string{seeded["Alicia"].ID, seeded["Berlin"].ID}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bankID,
		Items: []RetainItem{{
			Content:         "Alice met Berlin.",
			ResolveEntities: true,
			Entities:        []RetainEntity{{Text: "Alice"}, {Text: "Berlin"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, created := entityByName(store, bankID, "Alice"); created {
		t.Fatal("the shared neighbour must carry the near miss over the bar")
	}
	if _, found := entityByName(store, bankID, "Alicia"); !found {
		t.Fatal("the stored entity must survive the write")
	}
}

// The same shared neighbour is only evidence while it is selective: a hub seen
// beside several entities has its vote damped below what the name needs, so
// the mention creates its own entity.
func TestRetainHubDampingKeepsBoundaryCandidateApart(t *testing.T) {
	store := wiringStore(t)
	ctx := context.Background()
	const bankID = "wiring-hub"
	if _, err := store.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
		t.Fatal(err)
	}
	seeded := seedWiringEntities(t, ctx, store, bankID, "Alicia", "Berlin", "Carol", "Dave")
	for _, partner := range []string{"Alicia", "Carol", "Dave"} {
		if err := store.UpsertUnitCooccurrences(ctx, bankID, []string{seeded["Berlin"].ID, seeded[partner].ID}, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bankID,
		Items: []RetainItem{{
			Content:         "Alice met Berlin.",
			ResolveEntities: true,
			Entities:        []RetainEntity{{Text: "Alice"}, {Text: "Berlin"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, created := entityByName(store, bankID, "Alice"); !created {
		t.Fatal("a hub neighbour's damped vote must leave the near miss below the bar")
	}
}

// The candidate's last mention decides a near miss inside the window and does
// nothing beyond it: the same spelling merges when the candidate was seen a
// day ago and stays apart when it was seen over a week ago.
func TestRetainTemporalWindowDecidesBoundaryCandidate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lastSeen time.Duration
		merges   bool
	}{
		{name: "within the window", lastSeen: 24 * time.Hour, merges: true},
		{name: "beyond the window", lastSeen: 8 * 24 * time.Hour, merges: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := wiringStore(t)
			ctx := context.Background()
			const bankID = "wiring-temporal"
			if _, err := store.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
				t.Fatal(err)
			}
			seeded := seedWiringEntities(t, ctx, store, bankID, "Alicia")
			stamp := time.Now().UTC().Add(-tc.lastSeen)
			stored := store.entities[seeded["Alicia"].ID]
			stored.LastSeen = &stamp
			store.entities[seeded["Alicia"].ID] = stored
			now := time.Now().UTC()
			if _, err := store.RetainWrite(ctx, RetainRequest{
				BankID: bankID,
				Items: []RetainItem{{
					Content:         "Aliciaa checked in.",
					ResolveEntities: true,
					Timestamp:       &now,
					Entities:        []RetainEntity{{Text: "Aliciaa"}},
				}},
			}); err != nil {
				t.Fatal(err)
			}
			_, created := entityByName(store, bankID, "Aliciaa")
			if tc.merges && created {
				t.Fatal("the candidate seen inside the window must take the mention")
			}
			if !tc.merges && !created {
				t.Fatal("the candidate seen beyond the window must not take the mention")
			}
		})
	}
}

// A mention with no other entity beside it scores the name alone, however much
// co-occurrence evidence the bank holds about similar names.
func TestRetainWithoutNearbyScoresNameOnly(t *testing.T) {
	store := wiringStore(t)
	ctx := context.Background()
	const bankID = "wiring-lonely"
	if _, err := store.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
		t.Fatal(err)
	}
	seeded := seedWiringEntities(t, ctx, store, bankID, "Alicia", "Berlin")
	if err := store.UpsertUnitCooccurrences(ctx, bankID, []string{seeded["Alicia"].ID, seeded["Berlin"].ID}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bankID,
		Items: []RetainItem{{
			Content:         "Alice checked in.",
			ResolveEntities: true,
			Entities:        []RetainEntity{{Text: "Alice"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, created := entityByName(store, bankID, "Alice"); !created {
		t.Fatal("without nearby names the mention must score on the name alone")
	}
}
