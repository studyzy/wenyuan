package storage

import (
	"context"
	"strings"
	"testing"
)

// The column list a rename copies leaves `name` out of the plain carry: its
// value is chosen by the CASE that follows the identifier only when it still
// is the old identifier.
func TestBankNameCarriedFromID(t *testing.T) {
	columns := []string{"bank_id", "name", "config", "internal_id"}
	carried := bankNameCarriedFromID(columns, "$1", "$2")
	if len(carried) != 3 {
		t.Fatalf("carried = %v, want bank_id dropped and the rest kept", carried)
	}
	name := carried[0]
	if !strings.Contains(name, `"name" = $2`) || !strings.Contains(name, "THEN $1::text") {
		t.Fatalf("name expression = %q, want it keyed on the old identifier", name)
	}
	if carried[1] != `"config"` || carried[2] != `"internal_id"` {
		t.Fatalf("carried = %v, want the ordinary columns verbatim", carried)
	}
}

// A rename moves a display name that is still the identifier and leaves a
// chosen label alone.
func TestRenameBankCarriesTheIdentifierName(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	for _, bank := range []Bank{
		{BankID: "coding-agent::repo", Name: "coding-agent::repo"},
		{BankID: "coding-agent::named", Name: "文渊"},
	} {
		if _, err := store.CreateBank(ctx, bank); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RenameBank(ctx, "coding-agent::repo", "studyzy::repo"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameBank(ctx, "coding-agent::named", "studyzy::named"); err != nil {
		t.Fatal(err)
	}
	moved, err := store.GetBank(ctx, "studyzy::repo")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Name != "studyzy::repo" {
		t.Fatalf("name = %q, want it to follow the identifier", moved.Name)
	}
	kept, err := store.GetBank(ctx, "studyzy::named")
	if err != nil {
		t.Fatal(err)
	}
	if kept.Name != "文渊" {
		t.Fatalf("name = %q, want the chosen label untouched", kept.Name)
	}
}
