//go:build pgx

package storage

import (
	"context"
	"testing"
)

// A first write to a bank that was never declared must create the bank rather
// than fail on the foreign key its own operation row carries. Callers address a
// bank by name; declaring it first is not part of the contract.
func TestPostgresRetainLazilyCreatesTheBank(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	bankID := "lazy-bank-" + pgUUID("lazy")[:8]

	if _, err := store.(*PostgresStore).GetBank(ctx, bankID); err == nil {
		t.Fatal("the bank must not exist before the first write")
	}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bankID,
		Items:  []RetainItem{{Content: "Alice moved to Berlin."}},
	})
	if err != nil {
		t.Fatalf("retain into a fresh bank: %v", err)
	}
	if len(outcome.ItemUnitIDs) != 1 || len(outcome.ItemUnitIDs[0]) == 0 {
		t.Fatalf("the write produced no units: %+v", outcome)
	}
	bank, err := store.(*PostgresStore).GetBank(ctx, bankID)
	if err != nil {
		t.Fatalf("the bank row must exist once the write lands: %v", err)
	}
	// The default name is the bank's own identifier, which is what makes the
	// lazily created row indistinguishable from one created on purpose.
	if bank.Name != bankID {
		t.Fatalf("bank name = %q, want %q", bank.Name, bankID)
	}
	if bank.Mission != "" {
		t.Fatalf("a lazily created bank carries no mission, got %q", bank.Mission)
	}
}

// The lazy create is idempotent, so a write to an existing bank changes nothing
// about it and concurrent first writes cannot collide.
func TestPostgresEnsureBankExistsIsIdempotent(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	pg := store.(*PostgresStore)
	bankID := "lazy-bank-" + pgUUID("idem")[:8]

	created, err := pg.ensureBankExists(ctx, bankID)
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if !created {
		t.Fatal("the first ensure must report that it created the bank")
	}
	created, err = pg.ensureBankExists(ctx, bankID)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if created {
		t.Fatal("the second ensure must report that the bank already existed")
	}
	if _, err := pg.GetBank(ctx, bankID); err != nil {
		t.Fatalf("the bank must still be readable: %v", err)
	}
}
