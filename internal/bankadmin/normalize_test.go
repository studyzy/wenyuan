package bankadmin

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A bank under another deployment's prefix, and a bank with no prefix at all,
// both normalize under the tenant; a bank already under it keeps its name, so a
// second run is a no-op.
func TestNormalizedBankID(t *testing.T) {
	cases := []struct{ tenant, bankID, want string }{
		{"studyzy", "coding-agent::wenyuan", "studyzy::wenyuan"},
		{"studyzy", "coding-agent::OpenSpec-cn", "studyzy::OpenSpec-cn"},
		{"studyzy", "audit-kb-nope", "studyzy::audit-kb-nope"},
		{"studyzy", "other-tenant::repo", "studyzy::repo"},
		{"studyzy", "studyzy::myself", "studyzy::myself"},
		{"studyzy", "studyzy::coding-agent::x", "studyzy::coding-agent::x"},
	}
	for _, test := range cases {
		if got := NormalizedBankID(test.tenant, test.bankID); got != test.want {
			t.Errorf("NormalizedBankID(%q, %q) = %q, want %q", test.tenant, test.bankID, got, test.want)
		}
	}
}

// The plan names each bank once and leaves the already-normalized ones alone.
func TestPlanNormalizeBanks(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	for _, id := range []string{"coding-agent::wenyuan", "audit-kb-nope", "studyzy::myself"} {
		if _, err := store.CreateBank(ctx, storage.Bank{BankID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := PlanNormalizeBanks(ctx, store, "studyzy")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"coding-agent::wenyuan": "studyzy::wenyuan",
		"audit-kb-nope":         "studyzy::audit-kb-nope",
	}
	if len(plan.Renames) != len(want) {
		t.Fatalf("renames = %v, want %v", plan.Renames, want)
	}
	for source, target := range want {
		if plan.Renames[source] != target {
			t.Fatalf("rename of %q = %q, want %q", source, plan.Renames[source], target)
		}
	}
	if len(plan.Kept) != 1 || plan.Kept[0] != "studyzy::myself" {
		t.Fatalf("kept = %v, want the already-normalized bank", plan.Kept)
	}
}

// Two banks whose names differ only in their old prefix would collide under the
// new one; the plan reports it instead of letting the store refuse halfway.
func TestPlanNormalizeBanksRefusesCollision(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	for _, id := range []string{"coding-agent::repo", "old-host::repo"} {
		if _, err := store.CreateBank(ctx, storage.Bank{BankID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PlanNormalizeBanks(ctx, store, "studyzy"); err == nil {
		t.Fatal("a collision must be reported")
	}
}

// Running the normalization moves the data, not just the bank row: the facts
// written under the old identifier answer under the new one.
func TestRunNormalizeBanksMovesData(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "coding-agent::repo", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: "coding-agent::repo",
		Items:  []storage.RetainItem{{Content: "hello world"}},
	}); err != nil {
		t.Fatal(err)
	}
	before := store.UnitsForTest("coding-agent::repo")
	var out bytes.Buffer
	plan, err := RunNormalizeBanks(ctx, store, "studyzy", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Applied || len(plan.Renames) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	if got := store.UnitsForTest("studyzy::repo"); len(got) != len(before) {
		t.Fatalf("units under the new identifier = %d, want %d", len(got), len(before))
	}
	if _, err := store.GetBank(ctx, "coding-agent::repo"); !errors.Is(err, storage.ErrBankNotFound) {
		t.Fatalf("the old identifier must be gone: %v", err)
	}

	// A second run changes nothing.
	again, err := RunNormalizeBanks(ctx, store, "studyzy", false, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Renames) != 0 || len(again.Kept) != 1 {
		t.Fatalf("second run = %+v, want a no-op", again)
	}
}

// A dry run reports the plan and leaves every identifier alone.
func TestRunNormalizeBanksDryRun(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "coding-agent::repo", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	plan, err := RunNormalizeBanks(ctx, store, "studyzy", true, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied {
		t.Fatal("a dry run must not apply")
	}
	if _, err := store.GetBank(ctx, "coding-agent::repo"); err != nil {
		t.Fatalf("the source must survive a dry run: %v", err)
	}
}
