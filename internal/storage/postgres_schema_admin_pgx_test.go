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

func TestDropTenantSchemaProtectsSharedSchemas(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the drop-schema test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = db.Close() })

	for _, protected := range []string{"public", "admin"} {
		if err := DropTenantSchema(ctx, db, protected); !errors.Is(err, ErrSchemaProtected) {
			t.Fatalf("DropTenantSchema(%q) = %v, want ErrSchemaProtected", protected, err)
		}
	}
	if err := DropTenantSchema(ctx, db, "Admin"); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("DropTenantSchema(Admin) = %v, want ErrInvalidSchema", err)
	}
	if exists, err := SchemaExists(ctx, db, "public"); err != nil || !exists {
		t.Fatalf("public must survive: exists=%v err=%v", exists, err)
	}

	schema := fmt.Sprintf("drop_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
	})
	if err := ProvisionTenantSchema(ctx, db, schema, false, "pgvector"); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := DropTenantSchema(ctx, db, schema); err != nil {
		t.Fatalf("drop: %v", err)
	}
	exists, err := SchemaExists(ctx, db, schema)
	if err != nil || exists {
		t.Fatalf("schema survived the drop: exists=%v err=%v", exists, err)
	}
}

func TestMigrateTenantSchema(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the migrate-schema test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = db.Close() })

	schema := fmt.Sprintf("mig_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
	})
	if err := ProvisionTenantSchema(ctx, db, schema, false, "pgvector"); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := MigrateTenantSchema(ctx, db, schema, "pgvector"); err != nil {
		t.Fatalf("migrate existing schema: %v", err)
	}
	if err := MigrateTenantSchema(ctx, db, "admin", "pgvector"); !errors.Is(err, ErrSchemaProtected) {
		t.Fatalf("migrate admin = %v, want ErrSchemaProtected", err)
	}
}
