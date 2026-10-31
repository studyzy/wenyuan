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

// adminTeamMigrations names the admin migrations that only a multi-tenant
// deployment needs: the team registry (0002), the merge ledger (0003) and the
// team table corrections (0004).
//
// A single-tenant deployment has no teams, and nothing in it reads these
// tables, so creating them would put team vocabulary in the schema of a
// deployment that never uses teams. The split is by table rather than by
// migration, because a later multi-tenant migration may sit beside a
// tenant-independent one; naming the versions explicitly keeps the two from
// drifting together by accident.
//
// 0005 (the personal aggregate) is deliberately absent: folding a person's own
// banks together is not a team feature, so a single-tenant deployment gets it.
var adminTeamMigrations = map[int]bool{2: true, 3: true, 4: true}

// adminMigrationRequiredByMultiTenant reports whether version is a team
// migration, and so is skipped when the deployment is single-tenant.
func adminMigrationRequiredByMultiTenant(version int) bool { return adminTeamMigrations[version] }

// AdminMigrationSeries returns the admin schema's series in version order.
//
// It is the full series. Use AdminMigrationSeriesFor when the deployment's
// tenant mode decides whether the team migrations apply.
func AdminMigrationSeries() ([]Migration, error) {
	return migrationSeriesFrom(adminMigrationFS, "embeddedadmin")
}

// AdminMigrationSeriesFor returns the admin series a deployment in this tenant
// mode needs.
//
// A single-tenant deployment gets the tenant registry only; a multi-tenant one
// gets the team tables too. Skipping is "do not create", never "roll back": a
// deployment that was multi-tenant and is switched to single keeps the team
// rows it already has, recorded in schema_migrations, and they stay untouched.
func AdminMigrationSeriesFor(multiTenant bool) ([]Migration, error) {
	series, err := AdminMigrationSeries()
	if err != nil {
		return nil, err
	}
	if multiTenant {
		return series, nil
	}
	required := make([]Migration, 0, len(series))
	for _, migration := range series {
		if adminMigrationRequiredByMultiTenant(migration.Version) {
			continue
		}
		required = append(required, migration)
	}
	return required, nil
}

// MigrateAdminSchema creates and migrates the fixed admin schema. It never
// runs the reference series.
//
// It applies the full series, which is what a multi-tenant deployment wants and
// what the management plane's own tests exercise. A single-tenant deployment
// goes through MigrateAdminSchemaFor instead.
func MigrateAdminSchema(ctx context.Context, db DB) error {
	return MigrateAdminSchemaFor(ctx, db, true)
}

// MigrateAdminSchemaFor creates and migrates the fixed admin schema, applying
// only the migrations this tenant mode needs.
func MigrateAdminSchemaFor(ctx context.Context, db DB, multiTenant bool) error {
	return migrateAdminSchemaFor(ctx, db, AdminSchema, multiTenant)
}

// adminLedgerDDL is the registry's own migration bookkeeping. It is
// schema-qualified by the session's search_path, which the transaction sets.
const adminLedgerDDL = "CREATE TABLE IF NOT EXISTS schema_migrations (" +
	"version integer PRIMARY KEY, " +
	"name text NOT NULL, " +
	"applied_at timestamptz NOT NULL DEFAULT now())"

// migrateAdminSchemaFor applies the admin series a tenant mode needs. See
// AdminMigrationSeriesFor for what a single-tenant deployment skips.
func migrateAdminSchemaFor(ctx context.Context, db DB, schema string, multiTenant bool) error {
	series, err := AdminMigrationSeriesFor(multiTenant)
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
