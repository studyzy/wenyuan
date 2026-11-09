package storage

import (
	"context"
	"testing"
)

// The read preference has to be reachable from the query types, not only from
// FallbackQuery: a caller that sets a team scope and asks for the personal bank
// must be answered by its own bank, and that preference travels on RecallQuery
// and MemoryQuery into the helper.

// seedPreferenceFixture puts one distinguishable fact in the caller's bank and
// one in the team bank, so the test can say which bank answered.
func seedPreferenceFixture(t *testing.T) *MemoryStore {
	t.Helper()
	store := NewMemoryStore("public")
	if err := store.EnsureBank(context.Background(), "alice::Web"); err != nil {
		t.Fatalf("ensure personal bank: %v", err)
	}
	if err := store.EnsureBank(context.Background(), "acme::Web"); err != nil {
		t.Fatalf("ensure team bank: %v", err)
	}
	store.SeedUnit(MemoryUnit{
		ID: "11111111-0000-0000-0000-000000000001", BankID: "alice::Web",
		Text: "The deploy uses port 15432.", FactType: TypeWorld,
	})
	store.SeedUnit(MemoryUnit{
		ID: "22222222-0000-0000-0000-000000000002", BankID: "acme::Web",
		Text: "The team deploy uses port 15432.", FactType: TypeWorld,
	})
	return store
}

// TestRecallCarriesTheFallbackPreference pins that RecallQuery's preference
// reaches withFallback: a team scope plus PreferPersonal is answered by the
// caller's own bank, not the team's.
func TestRecallCarriesTheFallbackPreference(t *testing.T) {
	store := seedPreferenceFixture(t)
	scope := &FallbackScope{Schema: "public", BankID: "acme::Web"}
	result, err := store.Recall(context.Background(), RecallQuery{
		BankID:             "alice::Web",
		Query:              "deploy port",
		Fallback:           scope,
		FallbackPreference: PreferPersonal,
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Text != "The deploy uses port 15432." {
		t.Fatalf("Recall matches = %v, want only the caller's own fact", result.Matches)
	}
}

// TestRecallWithoutAPreferenceReadsTheTeamBank is the contrast: the same query
// with no preference keeps the team-first decision, so the test above fails for
// the reason it names rather than because the fallback never fires.
func TestRecallWithoutAPreferenceReadsTheTeamBank(t *testing.T) {
	store := seedPreferenceFixture(t)
	scope := &FallbackScope{Schema: "public", BankID: "acme::Web"}
	result, err := store.Recall(context.Background(), RecallQuery{
		BankID: "alice::Web", Query: "deploy port", Fallback: scope,
	})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Text != "The team deploy uses port 15432." {
		t.Fatalf("Recall matches = %v, want only the team's fact", result.Matches)
	}
}

// TestListMemoriesCarriesTheFallbackPreference pins the same reachability on the
// listing.
func TestListMemoriesCarriesTheFallbackPreference(t *testing.T) {
	store := seedPreferenceFixture(t)
	scope := &FallbackScope{Schema: "public", BankID: "acme::Web"}
	page, err := store.ListMemories(context.Background(), MemoryQuery{
		BankID:             "alice::Web",
		Fallback:           scope,
		FallbackPreference: PreferPersonal,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(page.Memories) != 1 || page.Memories[0].Text != "The deploy uses port 15432." {
		t.Fatalf("listing = %v, want only the caller's own fact", page.Memories)
	}
}
