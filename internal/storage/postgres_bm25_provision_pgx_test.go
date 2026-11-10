//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestProvisionTenantSchemaBuildsTheKeywordIndex pins the one thing a provisioned
// schema used to get wrong.
//
// The pg_search keyword index is not part of the migration series: it is
// reconciled after the series commits, from the store's own backend settings.
// A provisioning helper that built its store without those settings therefore
// produced a schema whose keyword arm failed at query time — "memory_units does
// not contain a USING paradedb index" — while every other assertion about the
// schema passed, because the tables themselves were all there.
func TestProvisionTenantSchemaBuildsTheKeywordIndex(t *testing.T) {
	dsn := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the keyword index provisioning test was not run")
	}
	db, err := OpenDatabaseWithBudget(dsn, PoolBudget{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// The backend the deployment selects decides whether there is an index at
	// all: pg_search needs the extension, so the case is skipped on a database
	// that does not carry it rather than failing for the wrong reason.
	var hasPGSearch bool
	if err := db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_search')").Scan(&hasPGSearch); err != nil {
		t.Fatalf("look for pg_search: %v", err)
	}
	if !hasPGSearch {
		t.Skip("pg_search is not available on this database; the keyword index test was not run")
	}
	// The extension installed under the pdb schema is what the index's column
	// casts (::pdb.jieba) resolve against, and a schema-scoped install leaves
	// that schema behind.
	if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector CASCADE"); err != nil {
		t.Fatalf("install pg_search: %v", err)
	}

	schema := fmt.Sprintf("kwidx%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
		_ = db.Close()
	})

	cfg := SchemaStoreConfig{
		VectorExtension:     "vector",
		TextSearchExtension: TextSearchPGSearch,
		PGTokenizer:         "jieba",
	}
	if err := ProvisionTenantSchema(ctx, db, schema, false, cfg); err != nil {
		t.Fatalf("ProvisionTenantSchema: %v", err)
	}

	var method string
	err = db.QueryRowContext(ctx,
		`SELECT am.amname FROM pg_index i
		   JOIN pg_class c ON c.oid = i.indexrelid
		   JOIN pg_am am ON am.oid = c.relam
		  WHERE c.relname = $1 AND c.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = $2)`,
		bm25IndexName, schema).Scan(&method)
	if err != nil {
		t.Fatalf("a provisioned pg_search schema has no %s index: %v", bm25IndexName, err)
	}
	if method != "bm25" {
		t.Fatalf("%s uses %q, want ParadeDB's bm25 access method", bm25IndexName, method)
	}
}
