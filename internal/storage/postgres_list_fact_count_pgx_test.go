//go:build pgx

package storage

import (
	"context"
	"testing"
)

// The collection carries fact_count so a listing can show how much each bank
// holds, and the reference fills it with the bank's memory_units rows. A page
// that leaves it zero reads as an empty bank, so this pins the value per bank
// and that the count is scoped to the banks on the page.
func TestPostgresListBanksFillsFactCount(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "list_fact_count")
	for _, bankID := range []string{"counted", "empty"} {
		if _, err := pg.CreateBank(ctx, Bank{BankID: bankID, Name: bankID}); err != nil {
			t.Fatal(err)
		}
	}
	// Three units spread over the fact types, so a count that filtered to one
	// type would come back short.
	units := []MemoryUnit{
		{ID: pgUUID("fc-1"), BankID: "counted", Text: "world fact", FactType: TypeWorld},
		{ID: pgUUID("fc-2"), BankID: "counted", Text: "experience fact", FactType: TypeExperience},
		{ID: pgUUID("fc-3"), BankID: "counted", Text: "an observation", FactType: TypeObservation},
	}
	for _, unit := range units {
		if _, err := pg.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}

	page, err := pg.ListBanks(ctx, BankQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, bank := range page.Banks {
		got[bank.BankID] = bank.FactCount
	}
	if got["counted"] != 3 {
		t.Fatalf("counted bank fact_count = %d, want 3 (every fact type counts)", got["counted"])
	}
	if got["empty"] != 0 {
		t.Fatalf("empty bank fact_count = %d, want 0", got["empty"])
	}

	// A page that does not include the counted bank must not count it either —
	// the query is scoped to the page, not aggregated over the table.
	narrow, err := pg.ListBanks(ctx, BankQuery{Search: "empty", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(narrow.Banks) != 1 || narrow.Banks[0].FactCount != 0 {
		t.Fatalf("narrowed page = %+v, want only the empty bank at 0", narrow.Banks)
	}
}

// The collection row carries the bank's config column, because the listing
// overlays that configuration on the legacy disposition and mission columns. A
// list row without it would report the columns the bank was created with rather
// than the traits its configuration names.
func TestPostgresListBanksCarriesConfig(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "list_config")
	if _, err := pg.CreateBank(ctx, Bank{
		BankID: "configured",
		Name:   "configured",
		Config: map[string]any{"disposition_skepticism": 5, "reflect_mission": "help"},
	}); err != nil {
		t.Fatal(err)
	}

	page, err := pg.ListBanks(ctx, BankQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Banks) != 1 {
		t.Fatalf("page = %+v, want the one bank", page.Banks)
	}
	if got := page.Banks[0].Config["disposition_skepticism"]; got != float64(5) {
		t.Fatalf("list config disposition_skepticism = %#v, want 5", got)
	}
	if got := page.Banks[0].Config["reflect_mission"]; got != "help" {
		t.Fatalf("list config reflect_mission = %#v, want help", got)
	}
}
