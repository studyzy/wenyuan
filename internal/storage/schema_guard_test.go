package storage

import (
	"os"
	"strings"
	"testing"
)

// TestDefaultSchemaIsOnlyReadByMigrationOrSchemaResolution is the static half
// of the multi-tenant safety net: every request path must go through
// schemaFor(ctx), so a direct read of the process schema fails the build's
// tests rather than silently serving the wrong tenant.
func TestDefaultSchemaIsOnlyReadByMigrationOrSchemaResolution(t *testing.T) {
	allowed := map[string]bool{
		"schema_context.go":      true, // schemaFor's single-tenant branch
		"postgres.go":            true, // migration series and extension setup
		"postgres_adoption.go":   true, // adoption reads the target schema
		"embedding_dimension.go": true, // migration reconciliation
		"admin_postgres.go":      true, // explicit-schema admin operations
		"admin_binary_pgx.go":    true, // explicit-schema backup/restore
		"db.go":                  true, // NewPostgresStore sets the field
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read storage dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(content), "defaultSchema") && !allowed[name] {
			t.Errorf("%s reads defaultSchema directly; use schemaFor(ctx)", name)
		}
	}
}
