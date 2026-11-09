//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// uniqueTestSchema names a schema for one test, so a failed run never collides
// with the next.
func uniqueTestSchema(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// openPGSearchTestStore opens a backend on the pg_search path in its own
// schema, and returns it once migrated.
//
// RunMigrations is left off here and Migrate is called after, which is what
// builds the keyword index: EnsureBM25Index runs at the end of a migration, so
// a store that never migrates never gets an index and the arm falls back to a
// sequential scan — correct but not what this test is pinning.
func openPGSearchTestStore(t *testing.T, ctx context.Context, url, schema string) *PostgresStore {
	t.Helper()
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{
		DatabaseURL: url, Schema: schema,
		TextSearchExtension: TextSearchPGSearch, PGTokenizer: "jieba",
	})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg, ok := store.(*PostgresStore)
	if !ok {
		t.Fatal("the postgres backend did not return a *PostgresStore")
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pg.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteIdent(schema)+` CASCADE`)
		_ = pg.Close()
	})
	return pg
}

// A Chinese sub-sentence term reaches the keyword arm through pg_search.
//
// This is the behaviour the native backend cannot provide. Its search_vector is
// built by PostgreSQL's own parser, which keeps a contiguous CJK run as one
// lexeme: a row reading "爱丽丝搬到了柏林" is reachable only by that exact run,
// and "柏林" matches nothing. pg_search's jieba tokenizer splits both the row
// and the query, so the sub-sentence term is a lexeme on both sides and meets.
//
// The test pins the whole path — the index EnsureBM25Index builds, the column
// casts its tokenizer, and the arm's `@@@` match — because each half can be
// right on its own while the pair still fails: an index built under one
// tokenizer and queried under another does not match at all.
func TestPostgresKeywordArmMatchesChineseSubstring(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the pg_search Chinese test was not run")
	}
	ctx := context.Background()
	schema := uniqueTestSchema(t, "bm25_cjk_pgsearch")
	store := openPGSearchTestStore(t, ctx, url, schema)

	const bank = "w18-cjk-pgsearch"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	for _, text := range []string{
		"爱丽丝搬到了柏林",
		"团队在深圳制定了发布计划",
	} {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: newID(), BankID: bank, Text: text, FactType: TypeWorld,
		}); err != nil {
			t.Fatalf("upsert unit %q: %v", text, err)
		}
	}

	// Each term is a fragment of one row and of no other. Under the native
	// backend every one of these matches nothing, which is the regression this
	// test exists to catch.
	for term, wantText := range map[string]string{
		"柏林":   "爱丽丝搬到了柏林",
		"爱丽丝":  "爱丽丝搬到了柏林",
		"深圳":   "团队在深圳制定了发布计划",
		"发布计划": "团队在深圳制定了发布计划",
	} {
		hits, err := store.keywordArm(ctx, RecallQuery{BankID: bank, Types: []string{string(TypeWorld)}}, term)
		if err != nil {
			t.Fatalf("keyword arm for %q: %v", term, err)
		}
		if len(hits) == 0 {
			t.Fatalf("keyword arm found no row for %q; the pg_search index is missing or built under another tokenizer", term)
		}
		if hits[0].match.Text != wantText {
			t.Fatalf("keyword arm for %q ranked %q first, want %q", term, hits[0].match.Text, wantText)
		}
		if hits[0].score <= 0 {
			t.Fatalf("keyword arm scored %q at %v, want a positive rank", term, hits[0].score)
		}
	}

	// An unrelated term still matches nothing, so the arm is not returning rows
	// unconditionally.
	hits, err := store.keywordArm(ctx, RecallQuery{BankID: bank, Types: []string{string(TypeWorld)}}, "量子计算")
	if err != nil {
		t.Fatalf("keyword arm for an unrelated term: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("an unrelated term matched %d rows, want 0", len(hits))
	}
}
