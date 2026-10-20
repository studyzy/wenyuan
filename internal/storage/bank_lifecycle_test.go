package storage

import (
	"context"
	"testing"
)

// ts-0001 / tmg-0001: the list page reports the pre-paging total and orders by
// recency; an offset past the end is empty with the real total.
func TestMemoryListBanksPaging(t *testing.T) {
	store := NewMemoryStore("public")
	for _, id := range []string{"a", "b", "c"} {
		if _, err := store.CreateBank(context.TODO(), Bank{BankID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ListBanks(context.TODO(), BankQuery{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Banks) != 2 {
		t.Fatalf("page = %+v", page)
	}
	past, err := store.ListBanks(context.TODO(), BankQuery{Limit: 2, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if past.Total != 3 || len(past.Banks) != 0 {
		t.Fatalf("past-the-end page = %+v", past)
	}
}

// list-0002: an explicit limit of 0 is a valid page size and returns nothing,
// while the total still reflects the match count.
func TestMemoryListBanksZeroLimit(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "a", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListBanks(context.TODO(), BankQuery{Limit: 0})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Banks) != 0 {
		t.Fatalf("limit=0 page = %+v, want empty with total 1", page)
	}
}

// ts-0004: the alias-attributed match is reported; a direct match reports none.
func TestMemoryListMatchedAliases(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "owner", Name: "Owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "owner", "nickname", false); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListBanks(context.TODO(), BankQuery{Search: "nickname", SearchGiven: true})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.MatchedAliases["owner"]) != 1 {
		t.Fatalf("alias match not attributed: %+v", page)
	}
	page, _ = store.ListBanks(context.TODO(), BankQuery{Search: "owner", SearchGiven: true})
	if len(page.MatchedAliases["owner"]) != 0 {
		t.Fatalf("direct match must report no matched aliases: %+v", page.MatchedAliases)
	}
}

// ts-0011 / inv-0002: promoting one alias demotes the other; the displayed
// alias sorts first.
func TestMemoryAliasPromotion(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "one", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "two", false); err != nil {
		t.Fatal(err)
	}
	aliases, err := store.SetAliasPrimary(context.TODO(), "b", "two", true)
	if err != nil {
		t.Fatal(err)
	}
	if aliases[0].Alias != "two" || !aliases[0].Primary {
		t.Fatalf("promotion must display the alias first: %+v", aliases)
	}
	primaries := 0
	for _, alias := range aliases {
		if alias.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("at most one alias may be displayed: %+v", aliases)
	}
}

// inv-0003: primary=false demotes just that alias, so the bank falls back to its
// own id and any other alias is left as it was.
func TestMemoryAliasDemotion(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "one", true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "two", false); err != nil {
		t.Fatal(err)
	}
	aliases, err := store.SetAliasPrimary(context.TODO(), "b", "one", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range aliases {
		if alias.Primary {
			t.Fatalf("primary=false must clear the display flag: %+v", aliases)
		}
	}
	if primary := store.PrimaryAlias(context.TODO(), "b"); primary != "" {
		t.Fatalf("bank must fall back to its own id, primary alias = %q", primary)
	}
}

// ts-0013: targeting an alias the bank does not own is a not-found.
func TestMemoryAliasNotFound(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAliasPrimary(context.TODO(), "b", "ghost", true); err != ErrAliasNotFound {
		t.Fatalf("promote unknown alias err = %v", err)
	}
	if _, err := store.RemoveAlias(context.TODO(), "b", "ghost"); err != ErrAliasNotFound {
		t.Fatalf("remove unknown alias err = %v", err)
	}
}

// ts-0012 / ec-0005: a self-alias is invalid and a colliding alias conflicts.
func TestMemoryAliasConflictAndSelfAlias(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "b", false); err != ErrInvalidBankID {
		t.Fatalf("self-alias err = %v", err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "other", false); err != ErrAliasConflict {
		t.Fatalf("alias colliding with a bank err = %v", err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "dup", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "other", "dup", false); err != ErrAliasConflict {
		t.Fatalf("duplicate alias err = %v", err)
	}
}

// beh-0009 / ts-0020: a write into a missing bank creates it lazily in the same
// operation, with an identifier-derived name, neutral disposition and empty
// mission.
func TestMemoryLazyBankCreationOnWrite(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.RecordWrite(context.TODO(), WriteRequest{BankID: "fresh", Content: "x"}); err != nil {
		t.Fatal(err)
	}
	bank, err := store.GetBank(context.TODO(), "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if bank.Name != "fresh" || bank.Mission != "" {
		t.Fatalf("lazily created bank = %+v", bank)
	}
	if bank.Trait(DispositionKeySkepticism) != DispositionTraitNeutral {
		t.Fatalf("lazily created bank must have a neutral disposition: %+v", bank)
	}
	if bank.LastWriteAt == nil {
		t.Fatal("a written bank must carry a write watermark")
	}
}

// ts-0010 / beh-0015: deleting a bank that never existed is a zero-item no-op.
func TestMemoryDeleteNeverCreatedIsNoOp(t *testing.T) {
	store := NewMemoryStore("public")
	deleted, err := store.DeleteBank(context.TODO(), "never")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0", deleted)
	}
}

// st-0003 / inv-0006: deleting a bank removes its alias rows. The reported
// count is content-based (units + entities + documents), so a bank that only
// carries an alias row reports zero.
func TestMemoryDeleteRemovesAliases(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "nick", true); err != nil {
		t.Fatal(err)
	}
	if deleted, err := store.DeleteBank(context.TODO(), "b"); err != nil || deleted != 0 {
		t.Fatalf("delete = %d, err = %v", deleted, err)
	}
	if resolved, _ := store.ResolveBankID(context.TODO(), "nick"); resolved != "nick" {
		t.Fatalf("an alias must not outlive its bank: %q", resolved)
	}
}

// st-0003 / inv-0006: the delete count is the bank's content, and the content
// is actually gone afterwards — memory_units and documents carry no foreign key
// to banks, so a cascade alone would orphan them.
func TestMemoryDeletePurgesContent(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDocument(context.TODO(), "b", "doc-1", Document{OriginalText: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(context.TODO(), MemoryUnit{ID: "unit-1", BankID: "b", FactType: TypeWorld, Text: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(context.TODO(), MemoryUnit{ID: "unit-2", BankID: "b", FactType: TypeWorld, Text: "two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveEntity(context.TODO(), "b", "Widget", EntityKindRegular, true); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteBank(context.TODO(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 4 {
		t.Fatalf("deleted = %d, want 4 (2 units + 1 entity + 1 document)", deleted)
	}
	stats, err := store.BankStats(context.TODO(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalNodes != 0 {
		t.Fatalf("a deleted bank must leave no memory units behind, got %d", stats.TotalNodes)
	}
	if _, err := store.GetBank(context.TODO(), "b"); err != ErrBankNotFound {
		t.Fatalf("GetBank after delete err = %v, want ErrBankNotFound", err)
	}
}

// neg-0004 / inv-0003: a name that is both a bank and an alias resolves to the
// bank.
func TestMemoryBankWinsOverAlias(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "real", Name: "Real"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "shared", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "shared", Name: "Shared"}); err != ErrAliasConflict {
		t.Fatalf("creating a bank under an existing alias err = %v", err)
	}
}

// tmg-0004 / beh-0014: a bank write invalidates the process cache entry.
func TestMemoryCacheInvalidatedOnWrite(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	bank, _ := store.GetBank(context.TODO(), "b")
	store.Cache().put(store.Schema(), "b", bank)
	if _, ok := store.Cache().get(store.Schema(), "b"); !ok {
		t.Fatal("the cache must serve a stored entry")
	}
	if _, err := store.UpdateBank(context.TODO(), "b", BankPatch{Name: strPtr("Renamed")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Cache().get(store.Schema(), "b"); ok {
		t.Fatal("a bank write must invalidate the cache entry")
	}
}

// sec-0001: two schemas that choose the same identifier never share an entry.
func TestMemoryCacheScopesBySchema(t *testing.T) {
	store := NewMemoryStore("public")
	store.Cache().put("tenant_a", "shared", Bank{BankID: "shared", Name: "A"})
	if _, ok := store.Cache().get("tenant_b", "shared"); ok {
		t.Fatal("a cache lookup must not leak across tenants")
	}
	if _, ok := store.Cache().get("tenant_a", "shared"); !ok {
		t.Fatal("the owning tenant must still find its entry")
	}
}

// ts-0022 / ts-0023: the migration series defines the alias table and both
// named indexes.
func TestMigrationDefinesAliasContract(t *testing.T) {
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	var sql string
	for _, migration := range series {
		sql += migration.SQL + "\n"
	}
	for _, needle := range []string{
		"bank_aliases", "idx_bank_aliases_bank", "idx_bank_aliases_one_primary",
		"bank_aliases_pkey",
	} {
		if !containsFold(sql, needle) {
			t.Fatalf("the migration series must define %q", needle)
		}
	}
}

func strPtr(s string) *string { return &s }
