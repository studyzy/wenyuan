package bankapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

func decodeAliases(t *testing.T, body []byte) AliasResponse {
	t.Helper()
	var out AliasResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// ts-0011: add, list, promote, demote and remove an alias, each returning the
// bank's own identifier and the full alias list; add returns the created status.
func TestAliasLifecycle(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "owner", Name: "Owner"})

	rec := post(t, mux, "/v1/default/banks/owner/aliases", `{"alias":"first","primary":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add alias status = %d, body = %s", rec.Code, rec.Body.String())
	}
	list := decodeAliases(t, rec.Body.Bytes())
	if list.BankID != "owner" {
		t.Fatalf("alias response must report the bank's own identifier: %+v", list)
	}
	if len(list.Aliases) != 1 || !list.Aliases[0].Primary {
		t.Fatalf("alias list = %+v", list.Aliases)
	}

	post(t, mux, "/v1/default/banks/owner/aliases", `{"alias":"second","primary":false}`)

	// Promoting "second" demotes "first"; the displayed alias is first in the list.
	rec = doJSON(t, mux, http.MethodPatch, "/v1/default/banks/owner/aliases/second", `{"primary":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote status = %d", rec.Code)
	}
	list = decodeAliases(t, rec.Body.Bytes())
	if len(list.Aliases) != 2 || list.Aliases[0].Alias != "second" || !list.Aliases[0].Primary {
		t.Fatalf("promotion must display the alias first and demote the other: %+v", list.Aliases)
	}
	if list.Aliases[1].Alias != "first" || list.Aliases[1].Primary {
		t.Fatalf("the previous display alias must be demoted: %+v", list.Aliases)
	}

	// Removing one drops it from the list.
	rec = doJSON(t, mux, http.MethodDelete, "/v1/default/banks/owner/aliases/first", "")
	list = decodeAliases(t, rec.Body.Bytes())
	if len(list.Aliases) != 1 || list.Aliases[0].Alias != "second" {
		t.Fatalf("removal must drop the alias: %+v", list.Aliases)
	}

	// Listing returns the same list.
	rec = doJSON(t, mux, http.MethodGet, "/v1/default/banks/owner/aliases", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list aliases status = %d", rec.Code)
	}
}

// ts-0012: a colliding alias is a conflict; a self-alias is a client error.
func TestAliasConflictAndSelfAlias(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "owner", Name: "Owner"})
	mustCreate(t, store, storage.Bank{ID: "other", Name: "Other"})
	post(t, mux, "/v1/default/banks/owner/aliases", `{"alias":"taken","primary":false}`)

	rec := post(t, mux, "/v1/default/banks/other/aliases", `{"alias":"taken","primary":false}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("colliding alias status = %d, want 409", rec.Code)
	}

	rec = post(t, mux, "/v1/default/banks/owner/aliases", `{"alias":"owner","primary":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("self-alias status = %d, want 400", rec.Code)
	}
}

// ts-0013: promoting or removing an alias the bank does not own is not-found.
func TestAliasNotFound(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "owner", Name: "Owner"})
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/owner/aliases/ghost", `{"primary":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("promote unknown alias status = %d", rec.Code)
	}
	if rec := doJSON(t, mux, http.MethodDelete, "/v1/default/banks/owner/aliases/ghost", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("remove unknown alias status = %d", rec.Code)
	}
}

// ts-0014 / beh-0008: every bank-scoped route accepts an alias and reports the
// canonical identifier.
func TestAliasResolvesAtEveryRoute(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "owner", Name: "Owner"})
	post(t, mux, "/v1/default/banks/owner/aliases", `{"alias":"nick","primary":false}`)

	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/nick", `{"mission":"via alias"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch through alias status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var profile BankResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.BankID != "owner" {
		t.Fatalf("a response reached through an alias must report the canonical id: %+v", profile)
	}

	// The alias routes also resolve through the alias path parameter.
	rec = post(t, mux, "/v1/default/banks/nick/aliases", `{"alias":"nick2","primary":false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("adding an alias through an alias status = %d, body = %s", rec.Code, rec.Body.String())
	}
	list := decodeAliases(t, rec.Body.Bytes())
	if list.BankID != "owner" {
		t.Fatalf("alias response through an alias must report the canonical id: %+v", list)
	}
}

// ts-0015: creating a bank under a name that is already an alias is a conflict.
func TestCreateBankUnderExistingAliasConflicts(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "owner", Name: "Owner"})
	post(t, mux, "/v1/default/banks/owner/aliases", `{"alias":"blocked","primary":false}`)

	rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/blocked", `{"name":"Blocked"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("create under an existing alias status = %d, want 409", rec.Code)
	}
}

// ts-0016: the list reports the displayed alias for a promoted bank and none
// otherwise.
func TestListReportsDisplayAlias(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "promoted", Name: "Promoted"})
	mustCreate(t, store, storage.Bank{ID: "plain", Name: "Plain"})
	post(t, mux, "/v1/default/banks/promoted/aliases", `{"alias":"shiny","primary":true}`)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks", "")
	var envelope struct {
		Banks []BankListRow `json:"banks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	display := map[string]string{}
	for _, row := range envelope.Banks {
		display[row.BankID] = row.DisplayAlias
	}
	if display["promoted"] != "shiny" {
		t.Fatalf("promoted bank must report its display alias: %v", display)
	}
	if display["plain"] != "" {
		t.Fatalf("a bank without a promoted alias must report none: %v", display)
	}
}

// ts-0017: the configuration read always answers; the write is gated.
func TestBankConfigReadAlwaysWriteGated(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{ID: "b1", Name: "One"})

	gated := NewHandlers(store)
	gated.WithConfigAPI(false)
	mux := newMux(gated)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks/b1/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("config read must always be served: %d", rec.Code)
	}
	var read ConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Config == nil || read.Overrides == nil {
		t.Fatalf("config read must carry config and overrides: %+v", read)
	}

	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config", `{"updates":{"reflect_mission":"x"}}`); rec.Code != http.StatusNotFound {
		t.Fatalf("config write must be gated off: %d", rec.Code)
	}

	ungated := NewHandlers(store)
	ungated.WithConfigAPI(true)
	mux2 := newMux(ungated)
	rec = doJSON(t, mux2, http.MethodPatch, "/v1/default/banks/b1/config", `{"updates":{"disposition_empathy":5,"reflect_mission":"live"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("config write must succeed when enabled: %d, body = %s", rec.Code, rec.Body.String())
	}
	bank, err := store.GetBank(context.TODO(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if bank.DispositionEmpathy != 5 || bank.ReflectMission != "live" {
		t.Fatalf("config write must apply the overrides: %+v", bank)
	}
}

// The configuration reset removes every bank-specific override, leaving the
// bank on the global configuration, and is gated like the write.
func TestBankConfigResetClearsOverrides(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mustCreate(t, store, storage.Bank{ID: "b1", Name: "One"})

	gated := NewHandlers(store)
	gated.WithConfigAPI(false)
	if rec := doJSON(t, newMux(gated), http.MethodDelete, "/v1/default/banks/b1/config", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("config reset must be gated off: %d", rec.Code)
	}

	ungated := NewHandlers(store)
	ungated.WithConfigAPI(true)
	mux := newMux(ungated)
	if rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b1/config",
		`{"updates":{"disposition_empathy":5,"reflect_mission":"live"}}`); rec.Code != http.StatusOK {
		t.Fatalf("seed config write: %d", rec.Code)
	}

	rec := doJSON(t, mux, http.MethodDelete, "/v1/default/banks/b1/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("config reset must succeed: %d, body = %s", rec.Code, rec.Body.String())
	}
	var reset ConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &reset); err != nil {
		t.Fatal(err)
	}
	if value, _ := reset.Overrides["disposition_empathy"].(float64); value != float64(storage.DispositionTraitNeutral) {
		t.Fatalf("reset must clear the overrides: %+v", reset.Overrides)
	}
	if reset.Overrides["reflect_mission"] != "" {
		t.Fatalf("reset must clear the mission: %+v", reset.Overrides)
	}
	bank, err := store.GetBank(context.TODO(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if bank.DispositionEmpathy != storage.DispositionTraitNeutral || bank.ReflectMission != "" {
		t.Fatalf("reset must persist the defaults: %+v", bank)
	}
}

func newMux(h *Handlers) *http.ServeMux {
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux
}
