package storage

import (
	"context"
	"errors"
	"testing"
)

// The created-bank hook fires once per newly-created bank, on both creation
// paths, and never for a row that already exists.
func TestBankCreateHookFiresOncePerCreatedBank(t *testing.T) {
	store := NewMemoryStore("public")
	var calls []string
	store.SetBankCreateHook(func(ctx context.Context, bankID string) error {
		calls = append(calls, bankID)
		// The hook runs without the store's write lock, so it can provision
		// through the same store without deadlocking.
		name := "provisioned"
		_, err := store.UpdateBank(ctx, bankID, BankPatch{Name: &name})
		return err
	})
	ctx := context.Background()

	if _, err := store.CreateBank(ctx, Bank{BankID: "a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	// An existing bank is touched, not created, so the hook must not fire.
	if err := store.EnsureBank(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureBank(ctx, "b"); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Fatalf("hook calls = %v, want [a b]", calls)
	}
	bank, err := store.GetBank(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if bank.Name != "provisioned" {
		t.Fatalf("the hook's own write did not land: name = %q", bank.Name)
	}
}

// A hook failure is logged and swallowed: the bank is created and the caller
// sees success.
func TestBankCreateHookFailureDoesNotFailCreate(t *testing.T) {
	store := NewMemoryStore("public")
	store.SetBankCreateHook(func(context.Context, string) error { return errors.New("boom") })

	if _, err := store.CreateBank(context.Background(), Bank{BankID: "a"}); err != nil {
		t.Fatalf("create = %v, want success despite the hook failure", err)
	}
	if err := store.EnsureBank(context.Background(), "b"); err != nil {
		t.Fatalf("ensure = %v, want success despite the hook failure", err)
	}
}

// With no hook installed, creation is unchanged.
func TestBankCreateWithoutHook(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), Bank{BankID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureBank(context.Background(), "b"); err != nil {
		t.Fatal(err)
	}
}
