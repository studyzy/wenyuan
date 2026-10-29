package storage

import (
	"errors"
	"testing"
)

// The '_' prefix is reserved so a store-owned backend can keep its own
// bookkeeping objects next to a bank's objects without escaping bank names;
// the reference refuses it for every new bank, alias and rename target
// (bank_utils.py validate_new_bank_id). Existing banks are never re-checked.
func TestValidBankIDRefusesTheReservedUnderscorePrefix(t *testing.T) {
	cases := []struct {
		id    string
		valid bool
	}{
		{"b1", true},
		{"_index", false},
		{"_probe_bank", false},
		{"bank_1", true},
		{"", false},
	}
	for _, tc := range cases {
		if got := ValidBankID(tc.id); got != tc.valid {
			t.Fatalf("ValidBankID(%q) = %v, want %v", tc.id, got, tc.valid)
		}
	}
}

// The admission rule holds at the write gates: creating and renaming into a
// reserved identifier are refused, while a bank that already exists is never
// re-checked.
func TestReservedUnderscoreRefusedAtTheWriteGates(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(t.Context(), Bank{BankID: "_probe_bank", Name: "reserved"}); !errors.Is(err, ErrInvalidBankID) {
		t.Fatalf("create = %v, want ErrInvalidBankID", err)
	}
	if err := store.EnsureBank(t.Context(), "_probe_bank"); !errors.Is(err, ErrInvalidBankID) {
		t.Fatalf("lazy create = %v, want ErrInvalidBankID", err)
	}
	if err := store.RenameBank(t.Context(), "src", "_probe_bank"); !errors.Is(err, ErrInvalidBankID) {
		t.Fatalf("rename = %v, want ErrInvalidBankID", err)
	}
	// The refusal is at admission: an existing bank under an ordinary name is
	// untouched, and a rename onto itself is still a no-op.
	if err := store.RenameBank(t.Context(), "src", "src"); !errors.Is(err, ErrBankNotFound) {
		t.Fatalf("rename of a missing bank = %v, want ErrBankNotFound", err)
	}
	if ValidBankID("_index") {
		t.Fatal("the reserved prefix must not pass the identifier rules")
	}
}
