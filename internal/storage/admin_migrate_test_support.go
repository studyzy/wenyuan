//go:build pgx

package storage

import "context"

// migrateAdminSchema is MigrateAdminSchema with an explicit schema, so the
// PostgreSQL tests can use a throwaway one without touching the fixed admin
// schema.
//
// It is behind the pgx tag because only those tests reach it; the untagged build
// has no caller and would report it unused.
func migrateAdminSchema(ctx context.Context, db DB, schema string) error {
	return migrateAdminSchemaFor(ctx, db, schema, true)
}
