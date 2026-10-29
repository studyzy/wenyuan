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

func TestProvisionTenantSchema(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the provisioning test was not run")
	}
	db, err := OpenDatabaseWithBudget(url, PoolBudget{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	schema := fmt.Sprintf("prov_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoteIdent(schema)+" CASCADE")
		_ = db.Close()
	})

	if err := ProvisionTenantSchema(ctx, db, schema, false, "pgvector"); err != nil {
		t.Fatalf("ProvisionTenantSchema: %v", err)
	}
	exists, err := SchemaExists(ctx, db, schema)
	if err != nil || !exists {
		t.Fatalf("SchemaExists = %v/%v, want true/nil", exists, err)
	}
	if err := ProvisionTenantSchema(ctx, db, schema, false, "pgvector"); !errors.Is(err, ErrSchemaExists) {
		t.Fatalf("second provision without adopt = %v, want ErrSchemaExists", err)
	}
	if err := ProvisionTenantSchema(ctx, db, schema, true, "pgvector"); err != nil {
		t.Fatalf("adopt provision: %v", err)
	}
	if err := ProvisionTenantSchema(ctx, db, "Admin", false, "pgvector"); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("invalid schema = %v, want ErrInvalidSchema", err)
	}
}
