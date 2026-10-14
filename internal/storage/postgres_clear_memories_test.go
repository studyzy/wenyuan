//go:build pgx

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// These exercise the two surfaces against a real database, because the row
// mechanics — the status-guarded delete and the unit/links/associations
// cascade — are decided by SQL, not by the in-memory backend.
func newClearMemoriesStore(t *testing.T, label string) (*PostgresStore, string) {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the clear/delete test was not run")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("%s_%d", label, time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg, schema
}

// A terminal operation is removed, a live one is refused, an unknown one is
// not found, and another bank's operation is invisible.
func TestPostgresDeleteOperation(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "del_op")
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b2", Name: "b2"}); err != nil {
		t.Fatal(err)
	}
	const terminal = "11111111-1111-1111-1111-111111111111"
	const live = "22222222-2222-2222-2222-222222222222"
	const foreign = "33333333-3333-3333-3333-333333333333"
	for _, op := range []Operation{
		{OperationID: terminal, BankID: "b1", OperationType: OperationTypeRetain, Status: StatusCompleted},
		{OperationID: live, BankID: "b1", OperationType: OperationTypeRetain, Status: StatusPending},
		{OperationID: foreign, BankID: "b2", OperationType: OperationTypeRetain, Status: StatusCompleted},
	} {
		if _, err := pg.PutOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := pg.DeleteOperation(ctx, "b1", terminal); err != nil {
		t.Fatalf("delete terminal: %v", err)
	}
	if _, err := pg.GetOperation(ctx, terminal); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("deleted operation still readable: %v", err)
	}
	if _, err := pg.DeleteOperation(ctx, "b1", live); !errors.Is(err, ErrOperationNotTerminal) {
		t.Fatalf("live operation delete = %v, want ErrOperationNotTerminal", err)
	}
	if _, err := pg.DeleteOperation(ctx, "b1", "44444444-4444-4444-4444-444444444444"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("unknown operation delete = %v, want ErrOperationNotFound", err)
	}
	// Scoped to the bank: b1 cannot delete b2's operation, and the row stays.
	if _, err := pg.DeleteOperation(ctx, "b1", foreign); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("cross-bank delete = %v, want ErrOperationNotFound", err)
	}
	if _, err := pg.GetOperation(ctx, foreign); err != nil {
		t.Fatalf("foreign operation must survive: %v", err)
	}
}

// Clearing removes units and what hangs off them, narrows by type, and leaves
// the bank, its entities and its documents in place.
func TestPostgresClearBankMemories(t *testing.T) {
	ctx := context.Background()
	pg, _ := newClearMemoriesStore(t, "clear_mem")
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutDocument(ctx, "b1", "doc-1", Document{OriginalText: "seed"}); err != nil {
		t.Fatal(err)
	}
	entity, err := pg.ResolveEntity(ctx, "b1", "Tuesday", EntityKindRegular, true)
	if err != nil {
		t.Fatal(err)
	}
	// A unit identifier is a uuid column in this schema, so the fixture uses
	// uuid-shaped ids rather than readable ones.
	const (
		worldID = "aaaaaaaa-0000-0000-0000-000000000001"
		expID   = "aaaaaaaa-0000-0000-0000-000000000002"
		obsOne  = "aaaaaaaa-0000-0000-0000-000000000003"
		obsTwo  = "aaaaaaaa-0000-0000-0000-000000000004"
	)
	for _, unit := range []MemoryUnit{
		{ID: worldID, BankID: "b1", Text: "world fact", FactType: TypeWorld, DocumentID: "doc-1"},
		{ID: expID, BankID: "b1", Text: "experience fact", FactType: TypeExperience},
		{ID: obsOne, BankID: "b1", Text: "observation", FactType: TypeObservation},
		{ID: obsTwo, BankID: "b1", Text: "another observation", FactType: TypeObservation},
	} {
		if _, err := pg.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.UpsertUnitEntity(ctx, obsOne, entity.ID); err != nil {
		t.Fatal(err)
	}
	if err := pg.UpsertLink(ctx, MemoryLink{
		BankID: "b1", FromUnitID: obsOne, ToUnitID: obsTwo, LinkType: LinkSemantic,
	}); err != nil {
		t.Fatal(err)
	}

	// A type-scoped clear removes only that type.
	removed, err := pg.ClearBankMemories(ctx, "b1", string(TypeObservation))
	if err != nil {
		t.Fatalf("scoped clear: %v", err)
	}
	if removed != 2 {
		t.Fatalf("scoped clear removed %d, want 2", removed)
	}

	// The bank, its document and its entity survive a clear.
	if _, err := pg.GetBank(ctx, "b1"); err != nil {
		t.Fatalf("bank must survive a clear: %v", err)
	}
	if _, err := pg.GetDocument(ctx, "b1", "doc-1"); err != nil {
		t.Fatalf("document must survive a clear: %v", err)
	}

	// Clearing everything removes the rest and reports the count.
	removed, err = pg.ClearBankMemories(ctx, "b1", "")
	if err != nil {
		t.Fatalf("clear all: %v", err)
	}
	if removed != 2 {
		t.Fatalf("clear all removed %d, want 2", removed)
	}
	page, err := pg.ListMemories(ctx, MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatalf("list after clear: %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("bank still holds %d memories after a clear", page.Total)
	}
	// A second clear of an already-empty bank is a zero, not an error.
	if removed, err := pg.ClearBankMemories(ctx, "b1", ""); err != nil || removed != 0 {
		t.Fatalf("repeat clear = (%d, %v), want (0, nil)", removed, err)
	}
}
