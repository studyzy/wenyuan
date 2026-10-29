package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
)

// queryer is the statement surface shared by a database handle and a
// transaction, so the catalogue read runs inside the transaction that holds the
// migration lock and sees the same snapshot the decision is made on.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// catalogueSnapshot is what the target schema already holds, read back from the
// catalogue rather than trusted from a bookkeeping table: a database the
// reference service migrated records its state in alembic_version, which this
// build does not read.
type catalogueSnapshot struct {
	tables      map[string]map[string]bool
	constraints map[string]bool
	indexes     map[string]bool
	functions   map[string]bool
	// alembicRevision is the revision the reference's own bookkeeping records,
	// empty when the database holds no such table.
	alembicRevision string
	// alembicPresent reports whether the reference's bookkeeping table is
	// there at all, which is what tells a reference-migrated database apart
	// from an untouched one.
	alembicPresent bool
}

// has reports whether the snapshot holds the expected object.
func (c *catalogueSnapshot) has(object ExpectedObject) bool {
	switch object.Kind {
	case ObjectTable:
		return c.tables[object.Name] != nil
	case ObjectColumn:
		return c.tables[object.Table] != nil && c.tables[object.Table][object.Name]
	case ObjectConstraint:
		return c.constraints[object.Table+"\x00"+object.Name]
	case ObjectIndex:
		return c.indexes[object.Name]
	case ObjectFunction:
		return c.functions[object.Name]
	}
	return false
}

// missingFrom lists the objects of an inventory the snapshot does not hold, in
// inventory order, so the first element is the object the series would have
// created first and the operator gets a stable answer rather than whichever the
// map happened to yield.
func missingFrom(objects []ExpectedObject, snapshot *catalogueSnapshot) []ExpectedObject {
	var missing []ExpectedObject
	for _, object := range objects {
		if !snapshot.has(object) {
			missing = append(missing, object)
		}
	}
	return missing
}

// schemaState is what the target schema is, once read: nothing of this build's
// schema is there yet, all of it is there, or some of it is.
type schemaState int

const (
	// schemaEmpty means no object of the series exists, so the series runs.
	schemaEmpty schemaState = iota
	// schemaComplete means every object of the series exists, so the series is
	// recorded as applied and nothing is created.
	schemaComplete
	// schemaPartial means some objects exist and some do not, so the database
	// is refused rather than adopted or re-created.
	schemaPartial
)

// classifySchema decides what the target database is, from the catalogue alone.
//
// The rule that keeps adoption honest: it is only ever reached when every
// object the series creates is already there. An empty database runs the
// series; a database holding some of the series' objects — a half-applied
// series, or a reference database at a revision this build does not serve — is
// refused, because neither path is safe: adopting would leave the missing
// migrations unapplied, and running the series would collide with the objects
// that are already there.
//
// The comparison is made against the structural objects, so an absent index
// does not decide the outcome; see ExpectedSchema.Structural for why.
func classifySchema(expected *ExpectedSchema, snapshot *catalogueSnapshot) (schemaState, []ExpectedObject) {
	structural := expected.Structural()
	carriesAnyTable := false
	for _, object := range structural {
		if object.Kind == ObjectTable && snapshot.has(object) {
			carriesAnyTable = true
			break
		}
	}
	if !carriesAnyTable {
		// The reference records its own schema state, so a database carrying
		// that bookkeeping and none of the tables is a reference database at a
		// revision this build does not serve, not an empty one.
		if snapshot.alembicPresent {
			return schemaPartial, missingFrom(structural, snapshot)
		}
		return schemaEmpty, nil
	}
	missing := missingFrom(structural, snapshot)
	if len(missing) == 0 {
		return schemaComplete, nil
	}
	return schemaPartial, missing
}

// readCatalogue reads the objects that exist in the store's schema.
func (s *PostgresStore) readCatalogue(ctx context.Context, q queryer) (*catalogueSnapshot, error) {
	snapshot := &catalogueSnapshot{
		tables:      map[string]map[string]bool{},
		constraints: map[string]bool{},
		indexes:     map[string]bool{},
		functions:   map[string]bool{},
	}

	// Every ordinary table with the columns it declares. The join is outer so
	// a table that declares no column is still seen as present.
	tableRows, err := q.QueryContext(ctx, `
		SELECT c.relname, a.attname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attribute a
		       ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')`, s.defaultSchema)
	if err != nil {
		return nil, fmt.Errorf("read the tables of schema %q: %w", s.defaultSchema, err)
	}
	if err := scanTableRows(tableRows, snapshot); err != nil {
		return nil, err
	}

	constraintRows, err := q.QueryContext(ctx, `
		SELECT rel.relname, con.conname
		FROM pg_constraint con
		JOIN pg_class rel ON rel.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = rel.relnamespace
		WHERE n.nspname = $1`, s.defaultSchema)
	if err != nil {
		return nil, fmt.Errorf("read the constraints of schema %q: %w", s.defaultSchema, err)
	}
	if err := scanPairs(constraintRows, func(table, name string) {
		snapshot.constraints[table+"\x00"+name] = true
	}); err != nil {
		return nil, err
	}

	indexRows, err := q.QueryContext(ctx, `
		SELECT ic.relname
		FROM pg_index i
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_class tc ON tc.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = tc.relnamespace
		WHERE n.nspname = $1`, s.defaultSchema)
	if err != nil {
		return nil, fmt.Errorf("read the indexes of schema %q: %w", s.defaultSchema, err)
	}
	if err := scanSingles(indexRows, func(name string) { snapshot.indexes[name] = true }); err != nil {
		return nil, err
	}

	functionRows, err := q.QueryContext(ctx, `
		SELECT p.proname
		FROM pg_proc p
		JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = $1`, s.defaultSchema)
	if err != nil {
		return nil, fmt.Errorf("read the functions of schema %q: %w", s.defaultSchema, err)
	}
	if err := scanSingles(functionRows, func(name string) { snapshot.functions[name] = true }); err != nil {
		return nil, err
	}

	if snapshot.tables["alembic_version"] != nil {
		snapshot.alembicPresent = true
		// The revision is reported, not enforced: the catalogue is what decides
		// whether the schema is the one this build serves, and a revision this
		// build has never heard of whose schema matches is still that schema.
		row := q.QueryRowContext(ctx, `SELECT version_num FROM `+quoteIdent(s.defaultSchema)+`.alembic_version LIMIT 1`)
		var revision sql.NullString
		if err := row.Scan(&revision); err == nil {
			snapshot.alembicRevision = revision.String
		}
	}
	return snapshot, nil
}

// scanTableRows fills the table map from a (table, column) result.
func scanTableRows(rows *sql.Rows, snapshot *catalogueSnapshot) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			table  string
			column sql.NullString
		)
		if err := rows.Scan(&table, &column); err != nil {
			return err
		}
		if snapshot.tables[table] == nil {
			snapshot.tables[table] = map[string]bool{}
		}
		if column.Valid {
			snapshot.tables[table][column.String] = true
		}
	}
	return rows.Err()
}

// scanPairs fills a two-column result.
func scanPairs(rows *sql.Rows, visit func(first, second string)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var first, second string
		if err := rows.Scan(&first, &second); err != nil {
			return err
		}
		visit(first, second)
	}
	return rows.Err()
}

// scanSingles fills a one-column result.
func scanSingles(rows *sql.Rows, visit func(value string)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		visit(value)
	}
	return rows.Err()
}

// adoptSeries records the whole series as applied without creating anything.
//
// It runs in the caller's transaction under the migration lock, so two
// processes starting together produce one set of rows rather than an error, and
// a failure leaves the ledger as it was.
//
// An index of the series the database does not carry is named rather than
// built: adoption must not lock a live table to create one, and the database is
// served without it, which is what the reference itself does when its vector
// indexes were created outside its migrations. The absence is reported because
// it is the one difference between this database and a freshly migrated one.
func (s *PostgresStore) adoptSeries(ctx context.Context, tx Tx, series []Migration, applied map[int]bool, expected *ExpectedSchema, snapshot *catalogueSnapshot) error {
	recorded := 0
	for _, migration := range series {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+quoteIdent(s.defaultSchema)+`.schema_migrations (version, name) VALUES ($1, $2)
			 ON CONFLICT (version) DO NOTHING`,
			migration.Version, migration.Name); err != nil {
			return fmt.Errorf("recording adopted migration %04d failed: %w", migration.Version, err)
		}
		if !applied[migration.Version] {
			recorded++
		}
	}
	revision := snapshot.alembicRevision
	if revision == "" {
		revision = "none"
	}
	slog.Info("adopted the existing schema instead of re-creating it",
		"schema", s.defaultSchema,
		"recorded_migrations", recorded,
		"total_migrations", len(series),
		"latest_version", series[len(series)-1].Version,
		"alembic_revision", revision)
	if missing := missingFrom(expected.Objects(), snapshot); len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for _, object := range missing {
			names = append(names, object.String())
		}
		slog.Warn("the adopted database does not carry every index of the series; it serves without them",
			"schema", s.defaultSchema, "missing", strings.Join(names, ", "))
	}
	return nil
}

// partialSchemaError describes why a database that carries some of the schema
// is refused, naming the first object the series would have to create.
//
// The revision is reported rather than enforced: the catalogue is what decides
// whether the schema is the one this build serves, so the revision is evidence
// for the operator, not a second rule that could disagree with the catalogue.
func (s *PostgresStore) partialSchemaError(missing []ExpectedObject, expected *ExpectedSchema, snapshot *catalogueSnapshot) error {
	total := len(expected.Structural())
	message := fmt.Sprintf(
		"database is not the schema this build serves: schema %q already carries %d of the %d objects the migration series creates, and the first missing one is %s",
		s.defaultSchema, total-len(missing), total, missing[0])
	if snapshot.alembicPresent {
		message += fmt.Sprintf(
			"; the database carries the reference service's own bookkeeping (alembic_version = %q), so it was migrated by a revision this build does not serve",
			snapshot.alembicRevision)
	}
	return fmt.Errorf("%s; refusing to adopt a partial schema, because adopting would leave the unrecorded migrations unapplied and running the series would collide with the objects already present. Import the data into an empty database as docs/MIGRATION.md describes, or point this build at one", message)
}
