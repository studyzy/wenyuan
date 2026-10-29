package bankapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// reservedUnderscoreSentence is the reference's own refusal for a new bank id
// or alias carrying the reserved prefix (bank_utils.py:353-356).
const reservedUnderscoreSentence = "Bank id must not start with '_': that prefix is reserved"

// The alias create route answers 400 with the upstream sentence: today the
// storage refusal of a reserved id surfaces through the self-alias mapping,
// which names the wrong rule.
func TestAliasCreateRefusesTheReservedPrefix(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{BankID: "owner", Name: "Owner"})

	rec := post(t, mux, "/v1/default/banks/owner/aliases", `{"alias":"_probe_bank","primary":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("alias create status = %d, body %s", rec.Code, rec.Body.String())
	}
	var refusal struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Detail != reservedUnderscoreSentence {
		t.Fatalf("detail = %q, want %q", refusal.Detail, reservedUnderscoreSentence)
	}
	aliases, err := store.ListAliases(t.Context(), "owner")
	if err != nil || len(aliases) != 0 {
		t.Fatalf("a refused alias must not be stored: %v (err %v)", aliases, err)
	}
}

// The create route refuses the reserved prefix with the same sentence, both on
// the explicit create and on the identifier admission it shares with the alias
// route.
func TestBankCreateRefusesTheReservedPrefix(t *testing.T) {
	mux, store := newTestServer(t)
	rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/_probe_bank", `{"name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create status = %d, body %s", rec.Code, rec.Body.String())
	}
	var refusal struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Detail != reservedUnderscoreSentence {
		t.Fatalf("detail = %q, want %q", refusal.Detail, reservedUnderscoreSentence)
	}
	if _, err := store.GetBank(t.Context(), "_probe_bank"); err == nil {
		t.Fatal("a refused bank must not be created")
	}
}
