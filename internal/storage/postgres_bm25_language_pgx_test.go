//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// The native keyword arm tokenizes with the deployment's configured dictionary,
// on both sides of the arm: the expression the write stores into
// memory_units.search_vector and the to_tsquery the arm matches with. This runs
// the real write path against a real database under a non-default dictionary
// (`simple`, which keeps stopwords and does not stem) and pins both halves: the
// stored vector is the dictionary's own, and a term only that dictionary keeps
// as a lexeme is found.
func TestPostgresKeywordArmHonoursTheConfiguredDictionary(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the PostgreSQL keyword test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	schema := fmt.Sprintf("bm25_lang_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{
		DatabaseURL: url, Schema: schema, RunMigrations: true, TextSearchNativeLanguage: "simple",
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

	const bank = "w17-dictionary"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	const text = "Alice was the runner"
	unit := MemoryUnit{ID: newID(), BankID: bank, Text: text, FactType: TypeWorld}
	if _, err := store.UpsertUnit(ctx, unit); err != nil {
		t.Fatalf("upsert unit: %v", err)
	}

	// The stored vector is the `simple` dictionary's, not english's: it keeps
	// the stopwords english drops and does not stem. Comparing inside the
	// database also proves the service did not write the english vector, which
	// would make the row unsearchable under the configured dictionary.
	var simple, english bool
	err = pg.db.QueryRowContext(ctx,
		`SELECT search_vector = to_tsvector('simple', text),
		        search_vector = to_tsvector('english', text)
		   FROM `+quoteIdent(schema)+`.memory_units WHERE id = $1`, unit.ID).Scan(&simple, &english)
	if err != nil {
		t.Fatalf("read the stored vector: %v", err)
	}
	if !simple || english {
		t.Fatalf("stored vector matches simple=%v english=%v, want the configured dictionary only", simple, english)
	}

	hits, err := pg.keywordArm(ctx, RecallQuery{BankID: bank, Types: []string{string(TypeWorld)}}, "the")
	if err != nil {
		t.Fatalf("keyword arm for a stopword lexeme: %v", err)
	}
	if len(hits) != 1 || hits[0].score <= 0 {
		t.Fatalf("the configured dictionary did not search its own lexeme: %+v", hits)
	}

	// A store over the same schema whose dictionary is the default finds
	// nothing for that term: `the` is a stopword english drops from the query.
	// The row is not merely unfindable by ranking — the arm's term selection
	// discards the token before any statement runs.
	englishStore, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open the default-dictionary store: %v", err)
	}
	defer func() { _ = englishStore.(*PostgresStore).Close() }()
	hits, err = englishStore.(*PostgresStore).keywordArm(
		ctx, RecallQuery{BankID: bank, Types: []string{string(TypeWorld)}}, "the")
	if err != nil {
		t.Fatalf("keyword arm under the default dictionary: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("the default dictionary matched a term it drops, %d hits", len(hits))
	}
}
