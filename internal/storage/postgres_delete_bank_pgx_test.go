//go:build pgx

package storage

import (
	"context"
	"testing"
)

// The memory domain carries no foreign key to banks — memory_units, documents
// and entities survive a plain row delete — so a bank delete has to purge them
// itself. This pins the count the envelope reports and the fact that nothing is
// left behind.
func TestPostgresDeleteBankPurgesContent(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "del_bank")
	if _, err := pg.CreateBank(ctx, Bank{BankID: "doomed", Name: "doomed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateBank(ctx, Bank{BankID: "kept", Name: "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutDocument(ctx, "doomed", "doc-1", Document{OriginalText: "body"}); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"unit-1", "unit-2"} {
		if _, err := pg.UpsertUnit(ctx, MemoryUnit{
			ID: pgUUID(label), BankID: "doomed", DocumentID: "doc-1", Text: "fact " + label, FactType: TypeWorld,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pg.ResolveEntity(ctx, "doomed", "Widget", EntityKindRegular, true); err != nil {
		t.Fatal(err)
	}
	// The neighbouring bank must be untouched by the purge.
	if _, err := pg.PutDocument(ctx, "kept", "doc-2", Document{OriginalText: "keep me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.UpsertUnit(ctx, MemoryUnit{
		ID: pgUUID("unit-3"), BankID: "kept", DocumentID: "doc-2", Text: "survivor", FactType: TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}

	deleted, err := pg.DeleteBank(ctx, "doomed")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 4 {
		t.Fatalf("deleted = %d, want 4 (2 units + 1 entity + 1 document)", deleted)
	}

	for _, table := range []string{"memory_units", "documents", "entities"} {
		var remaining int
		query := `SELECT COUNT(*) FROM ` + quoteIdent(pg.schema) + `.` + table + ` WHERE bank_id = 'doomed'`
		if err := pg.db.QueryRowContext(ctx, query).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining != 0 {
			t.Fatalf("%s still has %d row(s) for a deleted bank", table, remaining)
		}
	}
	if _, err := pg.GetBank(ctx, "doomed"); err != ErrBankNotFound {
		t.Fatalf("GetBank after delete err = %v, want ErrBankNotFound", err)
	}
	survivor, err := pg.BankStats(ctx, "kept")
	if err != nil {
		t.Fatal(err)
	}
	if survivor.TotalNodes != 1 || survivor.TotalDocuments != 1 {
		t.Fatalf("the neighbouring bank lost content: %+v", survivor)
	}
}

// A bank that never existed reports zero and does not fail.
func TestPostgresDeleteNeverCreatedBankIsZero(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "del_bank_missing")
	deleted, err := pg.DeleteBank(ctx, "never")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
}
