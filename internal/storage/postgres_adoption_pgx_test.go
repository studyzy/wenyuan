//go:build pgx

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The adoption path is what lets this binary be pointed at a database the
// reference service migrated itself: the schema is already this build's schema,
// but the reference records that fact in its own bookkeeping, so the series has
// to recognise the catalogue instead of running on top of it.

// adoptionStore opens a store in its own schema and registers its teardown. It
// runs no migration, so the test controls when the series runs.
func adoptionStore(t *testing.T) (*PostgresStore, string) {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the schema adoption test was not run")
	}
	schema := fmt.Sprintf("adopt_%d", time.Now().UnixNano())
	store, err := (&PostgresBackend{}).Open(context.Background(),
		BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	return pg, schema
}

// siblingStore opens a second store on the same schema, standing in for a
// second process of the same deployment.
func siblingStore(t *testing.T, schema string) *PostgresStore {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	store, err := (&PostgresBackend{}).Open(context.Background(),
		BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open second postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

// catalogueIdentities fingerprints every object of a schema by the identity the
// catalogue gives it. A run that created an object would give it a new identity
// even when the object's name and shape are the same, so an unchanged
// fingerprint is the proof that nothing was created.
func catalogueIdentities(t *testing.T, pg *PostgresStore) map[string]string {
	t.Helper()
	ctx := context.Background()
	identities := map[string]string{}
	collect := func(query string) {
		rows, err := pg.db.QueryContext(ctx, query, pg.Schema())
		if err != nil {
			t.Fatalf("read identities: %v", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatalf("scan identity: %v", err)
			}
			identities[key] = value
		}
	}
	collect(`SELECT 'relation:' || c.relname, c.oid::text
	         FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	         WHERE n.nspname = $1`)
	collect(`SELECT 'column:' || c.relname || '.' || a.attname, a.attrelid::text || '#' || a.attnum::text
	         FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid
	         JOIN pg_namespace n ON n.oid = c.relnamespace
	         WHERE n.nspname = $1 AND a.attnum > 0 AND NOT a.attisdropped`)
	collect(`SELECT 'constraint:' || rel.relname || '.' || con.conname, con.oid::text
	         FROM pg_constraint con JOIN pg_class rel ON rel.oid = con.conrelid
	         JOIN pg_namespace n ON n.oid = rel.relnamespace
	         WHERE n.nspname = $1`)
	collect(`SELECT 'function:' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')', p.oid::text
	         FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
	         WHERE n.nspname = $1`)
	return identities
}

// compareIdentities fails when the fingerprint changed between two reads.
func compareIdentities(t *testing.T, before, after map[string]string) {
	t.Helper()
	for key, value := range before {
		if after[key] != value {
			t.Errorf("%s changed identity from %q to %q; something was re-created", key, value, after[key])
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			t.Errorf("%s appeared; an object was created", key)
		}
	}
}

// ledgerRows returns the recorded versions and their names.
func ledgerRows(t *testing.T, pg *PostgresStore) map[int]string {
	t.Helper()
	rows, err := pg.db.QueryContext(context.Background(),
		`SELECT version, name FROM `+quoteIdent(pg.Schema())+`.schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	defer func() { _ = rows.Close() }()
	recorded := map[int]string{}
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			t.Fatalf("scan ledger row: %v", err)
		}
		recorded[version] = name
	}
	return recorded
}

// recordedVersions reads the versions the ledger holds, as the recorded set
// rather than the rows. A test that asserts "this migration was recorded" wants
// the set; a test that asserts the names wants ledgerRows.
func recordedVersions(t *testing.T, pg *PostgresStore) map[int]bool {
	t.Helper()
	recorded := map[int]bool{}
	for version := range ledgerRows(t, pg) {
		recorded[version] = true
	}
	return recorded
}

// clearLedger empties the migration ledger, so the next migrate finds an
// unrecorded series while the schema itself is untouched. It is how a database
// built by something other than this binary is simulated: the reference service
// has the same schema and no rows in this table.
func clearLedger(t *testing.T, pg *PostgresStore) {
	t.Helper()
	if _, err := pg.db.ExecContext(context.Background(),
		`DELETE FROM `+quoteIdent(pg.Schema())+`.schema_migrations`); err != nil {
		t.Fatalf("clear the ledger: %v", err)
	}
}

// captureHandler collects the log records emitted while it is installed.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// messages renders the captured records, attributes included, so a test can
// assert on what an operator would see.
func (h *captureHandler) messages() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, record := range h.records {
		line := record.Message
		record.Attrs(func(attr slog.Attr) bool {
			line += fmt.Sprintf(" %s=%v", attr.Key, attr.Value.Any())
			return true
		})
		out = append(out, line)
	}
	return out
}

// captureLogs installs a capturing logger and restores the previous one when
// the test ends.
func captureLogs(t *testing.T) *captureHandler {
	t.Helper()
	handler := &captureHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return handler
}

// A database that already carries the whole schema is adopted: the series is
// recorded without being run, so no object is created and no statement can
// fail on one that already exists.
func TestMigrateAdoptsACompleteSchemaWithoutCreatingAnything(t *testing.T) {
	pg, _ := adoptionStore(t)
	ctx := context.Background()

	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	before := catalogueIdentities(t, pg)

	// The schema is now complete but unrecorded, which is what a database the
	// reference service migrated looks like to this build.
	clearLedger(t, pg)

	logs := captureLogs(t)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("adopting migrate: %v", err)
	}
	after := catalogueIdentities(t, pg)
	compareIdentities(t, before, after)

	recorded := ledgerRows(t, pg)
	if len(recorded) != len(series) {
		t.Fatalf("the adoption recorded %d migrations, the series holds %d", len(recorded), len(series))
	}
	for _, migration := range series {
		if recorded[migration.Version] != migration.Name {
			t.Errorf("migration %d recorded as %q, want %q", migration.Version, recorded[migration.Version], migration.Name)
		}
	}
	adopted := false
	for _, message := range logs.messages() {
		if strings.Contains(message, "adopted") {
			adopted = true
		}
	}
	if !adopted {
		t.Fatalf("the adoption was not logged: %v", logs.messages())
	}

	// A further start is a ledger no-op rather than a second adoption.
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("third migrate: %v", err)
	}
	compareIdentities(t, before, catalogueIdentities(t, pg))
	if len(ledgerRows(t, pg)) != len(series) {
		t.Fatal("a further start changed the ledger")
	}
}

// A database holding some of the series' objects is refused, with the first
// missing object named, and nothing is written.
func TestMigrateRefusesAPartialSchema(t *testing.T) {
	pg, schema := adoptionStore(t)
	ctx := context.Background()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if _, err := pg.db.ExecContext(ctx,
		fmt.Sprintf("ALTER TABLE %s.banks DROP COLUMN mission", quoteIdent(schema))); err != nil {
		t.Fatalf("drop the column: %v", err)
	}
	clearLedger(t, pg)
	before := catalogueIdentities(t, pg)

	err := pg.Migrate(ctx)
	if err == nil {
		t.Fatal("a schema missing an expected column must be refused")
	}
	message := err.Error()
	for _, want := range []string{"mission", "banks", "partial"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not mention %q: %s", want, message)
		}
	}
	if recorded := ledgerRows(t, pg); len(recorded) != 0 {
		t.Fatalf("a refused start recorded %d migrations", len(recorded))
	}
	compareIdentities(t, before, catalogueIdentities(t, pg))
}

// A database that carries the reference's bookkeeping and none of this build's
// schema is a reference database at a revision this build does not serve, not
// an empty one: running the series there would leave the two schemas mixed.
func TestMigrateRefusesAReferenceSchemaWithoutTheTables(t *testing.T) {
	pg, schema := adoptionStore(t)
	ctx := context.Background()
	if _, err := pg.db.ExecContext(ctx,
		fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdent(schema))); err != nil {
		t.Fatalf("create the schema: %v", err)
	}
	if _, err := pg.db.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE %s.alembic_version (version_num character varying(32) NOT NULL)`,
		quoteIdent(schema))); err != nil {
		t.Fatalf("create the reference bookkeeping: %v", err)
	}
	if _, err := pg.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s.alembic_version (version_num) VALUES ('000000000000')`,
			quoteIdent(schema))); err != nil {
		t.Fatalf("seed the reference revision: %v", err)
	}

	err := pg.Migrate(ctx)
	if err == nil {
		t.Fatal("a database with the reference's bookkeeping and none of the schema must be refused")
	}
	for _, want := range []string{"async_operations", "000000000000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %s", want, err)
		}
	}
}

// An empty schema is the normal case: the series runs and every version is
// recorded.
func TestMigrateRunsTheSeriesOnAFreshSchema(t *testing.T) {
	pg, _ := adoptionStore(t)
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate a fresh schema: %v", err)
	}
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	recorded := ledgerRows(t, pg)
	if len(recorded) != len(series) {
		t.Fatalf("a fresh schema recorded %d migrations, the series holds %d", len(recorded), len(series))
	}
	for _, migration := range series {
		if recorded[migration.Version] != migration.Name {
			t.Errorf("migration %d recorded as %q, want %q", migration.Version, recorded[migration.Version], migration.Name)
		}
	}
}

// Two processes of one deployment start together and apply the same series to
// the same empty schema. Both must succeed: the series runs under an advisory
// lock, so the second sees the first's work rather than colliding with it.
func TestConcurrentStartsAreSafe(t *testing.T) {
	first, schema := adoptionStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const starters = 3
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		failure error
	)
	for i := range starters {
		store := first
		if i > 0 {
			store = siblingStore(t, schema)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Migrate(ctx); err != nil {
				mu.Lock()
				if failure == nil {
					failure = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failure != nil {
		t.Fatalf("a concurrent start failed: %v", failure)
	}
	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	recorded := ledgerRows(t, first)
	if len(recorded) != len(series) {
		t.Fatalf("the concurrent starts recorded %d migrations, the series holds %d", len(recorded), len(series))
	}
}

// The case the adoption path exists for: a database the reference service
// migrated itself. It carries the reference's schema and the reference's
// bookkeeping, so this build has to adopt it rather than re-create it.
//
// The test restores the ledger rows it added, so it leaves the reference
// database's own content as it found it. Point WENYUAN_REFERENCE_URL at a
// database the reference service has migrated to run it.
func TestMigrateAdoptsTheReferenceDatabase(t *testing.T) {
	url := os.Getenv("WENYUAN_REFERENCE_URL")
	if url == "" {
		t.Skip("WENYUAN_REFERENCE_URL is not set; the reference adoption test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	schema := "public"
	store, err := (&PostgresBackend{}).Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open the reference database: %v", err)
	}
	pg := store.(*PostgresStore)
	// Registered before the ledger cleanup, so it runs after it: the cleanup
	// writes to the database, and a pool closed first would make it a no-op.
	t.Cleanup(func() { _ = pg.Close() })

	series, err := MigrationSeries()
	if err != nil {
		t.Fatal(err)
	}
	recorded, ledgerPresent, err := existingLedgerRows(ctx, pg)
	if err != nil {
		t.Fatalf("read the reference ledger: %v", err)
	}
	// The adoption path is only exercised when this build has not recorded the
	// series, so any rows a previous run left for our versions are removed
	// first and restored at the end. Without this the test would pass without
	// testing anything on a second run.
	for _, migration := range series {
		if _, err := pg.db.ExecContext(ctx,
			`DELETE FROM `+quoteIdent(schema)+`.schema_migrations WHERE version = $1`,
			migration.Version); err != nil {
			t.Fatalf("prepare the reference ledger: %v", err)
		}
	}
	t.Cleanup(func() {
		// Put the reference database's own bookkeeping back exactly as it was:
		// the table is dropped when it was not there, and the rows that were
		// there are restored. A failure here is reported rather than swallowed,
		// because a leftover row would claim a migration the reference database
		// never recorded.
		ctx := context.Background()
		if !ledgerPresent {
			if _, err := pg.db.ExecContext(ctx,
				`DROP TABLE IF EXISTS `+quoteIdent(schema)+`.schema_migrations`); err != nil {
				t.Errorf("drop the adoption ledger from %s: %v", schema, err)
			}
			return
		}
		for _, migration := range series {
			if _, err := pg.db.ExecContext(ctx,
				`DELETE FROM `+quoteIdent(schema)+`.schema_migrations WHERE version = $1`,
				migration.Version); err != nil {
				t.Errorf("remove the ledger row this run added in %s: %v", schema, err)
			}
		}
		for version, row := range recorded {
			if _, err := pg.db.ExecContext(ctx,
				`INSERT INTO `+quoteIdent(schema)+`.schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)
				 ON CONFLICT (version) DO NOTHING`, version, row.name, row.appliedAt); err != nil {
				t.Errorf("restore the ledger row %d in %s: %v", version, schema, err)
			}
		}
	})

	before := catalogueIdentities(t, pg)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("the reference database was not adopted: %v", err)
	}
	after := catalogueIdentities(t, pg)
	// The ledger table is the only object this build may add.
	delete(before, "relation:schema_migrations")
	delete(after, "relation:schema_migrations")
	compareIdentities(t, before, after)

	adopted := ledgerRows(t, pg)
	if len(adopted) != len(series) {
		t.Fatalf("the reference database records %d migrations, the series holds %d", len(adopted), len(series))
	}
}

// ledgerRow is one row of the migration ledger, as a database holds it. The
// timestamp is carried so a test that must put a foreign database's bookkeeping
// back can restore the row rather than re-stamp it.
type ledgerRow struct {
	name      string
	appliedAt time.Time
}

// existingLedgerRows reports which migrations a database already records and
// whether it has the ledger table at all.
func existingLedgerRows(ctx context.Context, pg *PostgresStore) (map[int]ledgerRow, bool, error) {
	rows, err := pg.db.QueryContext(ctx,
		`SELECT version, name, applied_at FROM `+quoteIdent(pg.Schema())+`.schema_migrations`)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return map[int]ledgerRow{}, false, nil
		}
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	recorded := map[int]ledgerRow{}
	for rows.Next() {
		var (
			version   int
			name      string
			appliedAt time.Time
		)
		if err := rows.Scan(&version, &name, &appliedAt); err != nil {
			return nil, false, err
		}
		recorded[version] = ledgerRow{name: name, appliedAt: appliedAt}
	}
	return recorded, true, rows.Err()
}

var _ queryer = (*sql.Tx)(nil)
