package storage

import (
	"context"
	"embed"
	"fmt"
	"strings"
)

// adminMigrationFS embeds the control plane's own migration series. It is a
// separate directory from migrations/ and embedded/ because that series is
// regenerated from the reference database and wipes every *.sql it finds.
//
//go:embed all:embeddedadmin
var adminMigrationFS embed.FS

// AdminSchema is the fixed schema holding the control plane's tables.
const AdminSchema = "admin"

// AdminMigrationSeries returns the admin schema's series in version order.
func AdminMigrationSeries() ([]Migration, error) {
	return migrationSeriesFrom(adminMigrationFS, "embeddedadmin")
}

// MigrateAdminSchema creates and migrates the fixed admin schema. It never
// runs the reference series.
func MigrateAdminSchema(ctx context.Context, db DB) error {
	return migrateAdminSchema(ctx, db, AdminSchema)
}

// adminLedgerDDL is the registry's own migration bookkeeping. It is
// schema-qualified by the session's search_path, which the transaction sets.
const adminLedgerDDL = "CREATE TABLE IF NOT EXISTS schema_migrations (" +
	"version integer PRIMARY KEY, " +
	"name text NOT NULL, " +
	"applied_at timestamptz NOT NULL DEFAULT now())"

// migrateAdminSchema is MigrateAdminSchema with an explicit schema, so tests
// can use a throwaway one without touching the fixed admin schema.
func migrateAdminSchema(ctx context.Context, db DB, schema string) error {
	series, err := AdminMigrationSeries()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('wenyuan:admin-migrations'))"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(schema)); err != nil {
		return fmt.Errorf("create admin schema %q: %w", schema, err)
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL search_path TO "+quoteIdent(schema)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, adminLedgerDDL); err != nil {
		return err
	}
	applied, err := adminAppliedMigrationsTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, migration := range series {
		if applied[migration.Version] {
			continue
		}
		for _, statement := range migration.Statements() {
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply admin migration %04d_%s: %w", migration.Version, migration.Name, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)",
			migration.Version, migration.Name); err != nil {
			return fmt.Errorf("record admin migration %04d_%s: %w", migration.Version, migration.Name, err)
		}
	}
	return tx.Commit()
}

// adminAppliedMigrationsTx reads the admin ledger inside the migration
// transaction, so the decision and the writes see one snapshot.
func adminAppliedMigrationsTx(ctx context.Context, tx Tx) (map[int]bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}
