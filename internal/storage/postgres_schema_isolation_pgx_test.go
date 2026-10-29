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

func TestPostgresSchemaRoutingIsolatesTenants(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the schema-routing test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schemaA := fmt.Sprintf("iso_a_%d", time.Now().UnixNano())
	schemaB := fmt.Sprintf("iso_b_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, schema := range []string{schemaA, schemaB} {
			_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
		}
		_ = db.Close()
	})

	for _, schema := range []string{schemaA, schemaB} {
		if err := NewPostgresStore(db, schema).Migrate(ctx); err != nil {
			t.Fatalf("migrate %s: %v", schema, err)
		}
	}

	store := NewPostgresStore(db, schemaA)
	store.requireSchema = true

	ctxA, err := WithSchema(context.Background(), schemaA)
	if err != nil {
		t.Fatalf("WithSchema(A): %v", err)
	}
	ctxB, err := WithSchema(context.Background(), schemaB)
	if err != nil {
		t.Fatalf("WithSchema(B): %v", err)
	}

	if _, err := store.CreateBank(ctxA, Bank{BankID: "shared", Name: "A"}); err != nil {
		t.Fatalf("CreateBank(A): %v", err)
	}
	if _, err := store.CreateBank(ctxB, Bank{BankID: "shared", Name: "B"}); err != nil {
		t.Fatalf("CreateBank(B): %v", err)
	}

	bankA, err := store.GetBank(ctxA, "shared")
	if err != nil {
		t.Fatalf("GetBank(A): %v", err)
	}
	if bankA.Name != "A" {
		t.Fatalf("bank A name = %q, want A", bankA.Name)
	}
	bankB, err := store.GetBank(ctxB, "shared")
	if err != nil {
		t.Fatalf("GetBank(B): %v", err)
	}
	if bankB.Name != "B" {
		t.Fatalf("bank B name = %q, want B", bankB.Name)
	}

	if _, err := store.GetBank(context.Background(), "shared"); !errors.Is(err, ErrSchemaRequired) {
		t.Fatalf("GetBank without a schema = %v, want ErrSchemaRequired", err)
	}
}
