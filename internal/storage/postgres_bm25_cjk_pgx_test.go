//go:build pgx

package storage

import (
	"context"
	"testing"
)

// A Chinese query reaches the keyword arm.
//
// The native backend indexes with to_tsvector('english', ...): the default
// parser keeps a contiguous CJK run as a single lexeme, so a row whose text is
// one CJK run is searchable only by that same run. The query side tokenizes the
// raw text and OR-joins the tokens into the tsquery, so the same run produced by
// each must meet — the arm cannot find nothing merely because the query is multi
// byte. This pins the whole path (the column expression UpsertUnit writes, the
// query tokenizer, and the arm's match) for CJK.
func TestPostgresKeywordArmMatchesCJKQuery(t *testing.T) {
	store := postgresVectorStore(t)
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatal("postgresVectorStore did not return a *PostgresStore")
	}
	ctx := context.Background()
	const bank = "w16-cjk-keyword"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatalf("create bank: %v", err)
	}

	run := "爱丽丝搬到了柏林"
	spaced := "北京 上海 项目 已发布"
	for _, text := range []string{run, spaced} {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: newID(), BankID: bank, Text: text, FactType: TypeWorld,
		}); err != nil {
			t.Fatalf("upsert unit %q: %v", text, err)
		}
	}

	// Each case names a term the row actually stores as its own CJK lexeme: the
	// whole run of the first row, one spaced run of the second.
	for _, term := range []string{run, "上海"} {
		hits, err := pg.keywordArm(ctx, RecallQuery{BankID: bank, Types: []string{string(TypeWorld)}}, term)
		if err != nil {
			t.Fatalf("keyword arm for %q: %v", term, err)
		}
		if len(hits) == 0 {
			t.Fatalf("keyword arm found no row for %q", term)
		}
		if hits[0].score <= 0 {
			t.Fatalf("keyword arm scored %q at %v, want a positive rank", term, hits[0].score)
		}
	}

	// A term that is not a stored lexeme matches nothing, so the arm is not
	// simply returning rows unconditionally.
	hits, err := pg.keywordArm(ctx, RecallQuery{BankID: bank, Types: []string{string(TypeWorld)}}, "柏林")
	if err != nil {
		t.Fatalf("keyword arm for a non-lexeme: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("a non-lexeme CJK term matched %d rows, want 0", len(hits))
	}
}
