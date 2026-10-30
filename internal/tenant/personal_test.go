package tenant

import "testing"

func TestPersonalBankID(t *testing.T) {
	if got := PersonalBankID("alice"); got != "alice::myself" {
		t.Fatalf("PersonalBankID = %q", got)
	}
	if !IsPersonalBankID("alice::myself") {
		t.Fatal("alice::myself must be the aggregate bank")
	}
	for _, id := range []string{
		"alice::myself2",
		"alice::MySelf",
		"alice::order-service",
		"::myself",
		"myself",
		"alice::myself::x",
	} {
		if IsPersonalBankID(id) {
			t.Fatalf("%q was taken for the aggregate bank", id)
		}
	}
}

// TestMySelfIsNotAReservedBankName pins the product decision behind the suffix:
// a project whose id is "myself" owns `alice::myself` as an ordinary project bank,
// so nothing may treat the *name* as reserved. Only the aggregate, produced in the
// person's own schema, is the rollup's.
func TestMySelfIsNotAReservedBankName(t *testing.T) {
	if err := ValidateProjectID("myself"); err != nil {
		t.Fatalf("project id myself is refused: %v", err)
	}
	if err := ValidateProjectName("myself"); err != nil {
		t.Fatalf("project name myself is refused: %v", err)
	}
	// The project bank and the aggregate share a spelling; that is exactly why the
	// rollup never takes a user row for its own and why the merge job excludes it.
	if ProjectBankID("alice", "myself") != PersonalBankID("alice") {
		t.Fatal("the project bank and the aggregate must spell the same id")
	}
}

func TestLiveLedgerKey(t *testing.T) {
	if !LiveLedgerKey(MergeKey("a fact")) {
		t.Fatal("a supported key must count as live")
	}
	if LiveLedgerKey(DroppedMergeKey) || LiveLedgerKey("") {
		t.Fatal("a dropped or empty key must not count as live")
	}
}
