package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrDriverUnavailable is returned when the PostgreSQL backend is selected but
// no database driver has been registered.
var ErrDriverUnavailable = errors.New("no database driver registered; build the pgx driver file to enable PostgreSQL")

// PostgresBackend opens a PostgreSQL-backed Store.
type PostgresBackend struct{}

// Open connects to PostgreSQL using the configured URL and schema. The actual
// driver is supplied through the registered OpenDBFunc so this file carries no
// third-party import.
func (b *PostgresBackend) Open(ctx context.Context, cfg BackendConfig) (Store, error) {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return nil, errors.New("database url is required for the postgres backend")
	}
	if !DriverAvailable() {
		return nil, ErrDriverUnavailable
	}
	// The URL is normalized before the driver sees it, so a string copied from
	// the reference's own configuration (SQLAlchemy scheme, `ssl=` parameter)
	// connects rather than failing on a scheme the driver does not know.
	db, err := openDB(NormalizeDatabaseURL(cfg.DatabaseURL), PoolBudget{
		MaxOpenConns:             cfg.MaxOpenConns,
		MaxIdleConns:             cfg.MaxIdleConns,
		Session:                  cfg.Session,
		AcquireTimeout:           cfg.AcquireTimeout,
		PoolSlowAcquireThreshold: cfg.PoolSlowAcquireThreshold,
		AcquireWarnThreshold:     cfg.AcquireWarnThreshold,
	})
	if err != nil {
		return nil, err
	}
	// A deployment can point the migration series at a role that may run DDL
	// while serving over a role that may not: the series then runs over its own
	// handle, and the serving pool never holds a connection that issued DDL.
	var migrationDB DB
	if strings.TrimSpace(cfg.MigrationDatabaseURL) != "" {
		migrationDB, err = openDB(NormalizeDatabaseURL(cfg.MigrationDatabaseURL), PoolBudget{
			MaxOpenConns: 2,
			MaxIdleConns: 1,
		})
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("opening the migration database: %w", err)
		}
	}
	// A separate read handle is the reference's read pool: reads that tolerate a
	// replica run there, so the primary's connections are left for writes. Its
	// session settings are the primary's — a replica that takes a statement
	// timeout has to be told the same one.
	var readDB DB
	if strings.TrimSpace(cfg.ReadDatabaseURL) != "" {
		readDB, err = openDB(NormalizeDatabaseURL(cfg.ReadDatabaseURL), PoolBudget{
			MaxOpenConns:             cfg.ReadPoolMax,
			MaxIdleConns:             cfg.ReadPoolMin,
			Session:                  cfg.Session,
			AcquireTimeout:           cfg.AcquireTimeout,
			PoolSlowAcquireThreshold: cfg.PoolSlowAcquireThreshold,
			AcquireWarnThreshold:     cfg.AcquireWarnThreshold,
		})
		if err != nil {
			_ = db.Close()
			if migrationDB != nil {
				_ = migrationDB.Close()
			}
			return nil, fmt.Errorf("opening the read database: %w", err)
		}
		if err := readDB.Ping(ctx); err != nil {
			_ = db.Close()
			if migrationDB != nil {
				_ = migrationDB.Close()
			}
			return nil, err
		}
	}
	// The text-search backend is validated here rather than trusted: the
	// pg_search arm embeds its function schema and tokenizer directly into SQL,
	// so both have to be known-safe before any statement is built.
	textSearch, err := resolveTextSearchBackend(cfg)
	if err != nil {
		_ = db.Close()
		if migrationDB != nil {
			_ = migrationDB.Close()
		}
		if readDB != nil {
			_ = readDB.Close()
		}
		return nil, err
	}
	if err := db.Ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Some statements in this package render client-supplied values as SQL
	// literals — the compound tag predicate and the per-bank vector index DDL,
	// which a utility statement cannot take as bind parameters. Escaping by
	// doubling the quote is only complete when the server treats backslashes
	// literally, so a server configured otherwise is refused here rather than
	// escaping values differently from every other query.
	if err := requireStandardConformingStrings(db.QueryRowContext(ctx, "SHOW standard_conforming_strings")); err != nil {
		_ = db.Close()
		return nil, err
	}
	schema := cfg.Schema
	if strings.TrimSpace(schema) == "" {
		schema = "public"
	}
	return &PostgresStore{
		db: db, schema: schema, vectorExtension: cfg.VectorExtension,
		bankStatsCacheTTL: cfg.BankStatsCacheTTL, migrationDB: migrationDB, readDB: readDB,
		textSearchExtension: textSearch.extension, pgFunctionSchema: textSearch.functionSchema,
		pgTokenizer: textSearch.tokenizer, tuning: DefaultTuning(),
	}, nil
}

// PostgresStore is the database-backed Store. It is the sole owner of the
// memory-domain tables.
type PostgresStore struct {
	db     DB
	schema string
	// vectorExtension names the index access method family the deployment runs
	// on, so the per-bank vector index DDL matches the stored vectors.
	vectorExtension string
	// bankStatsCacheTTL is the freshness window for a cached bank-statistics
	// summary. Zero disables the cache.
	bankStatsCacheTTL time.Duration
	// objectStore, when set, holds the attachment and archive payloads
	// outside the database. It is an interface so the storage area carries no
	// object-store import, and the composition root supplies the
	// implementation.
	objectStore ObjectStore
	// observationEmbedder, when set, embeds the text of an observation the
	// consolidation pass writes. Without it an observation carries no vector
	// and the vector recall arm cannot rank it, so recall would reach the
	// synthesis only by keyword.
	observationEmbedder EmbeddingGenerator
	// autoConsolidation is the deployment default for whether a curation edit
	// submits the follow-up consolidation pass. A bank's own
	// enable_auto_consolidation override wins over it. It mirrors the reference,
	// which submits consolidation after an edit only when the resolved bank
	// config enables it.
	autoConsolidation bool
	// tuning carries the resolved retrieval, write and reflection knobs, so a
	// deployment's configuration reaches the pipeline.
	tuning Tuning
	// migrationDB, when set, is the handle the migration series runs over
	// instead of the serving handle. Same database, different credentials.
	migrationDB DB
	// readDB, when set, is the read-only handle the store's read paths run
	// over: a replica the deployment pointed ReadDatabaseURL at. Nil means every
	// statement — read and write — runs on the primary.
	readDB DB
	// textSearchExtension names the BM25 backend: "native" (tsvector, the
	// default) or "pg_search" (ParadeDB). pgFunctionSchema and pgTokenizer are
	// pg_search's function schema and tokenizer, embedded into the keyword arm's
	// SQL.
	textSearchExtension string
	pgFunctionSchema    string
	pgTokenizer         string
	// recallGate bounds concurrent recalls at the engine level, separately from
	// admission control. A nil gate is unbounded.
	recallGate chan struct{}
	// annRejected remembers the pgvector settings this server refused, so a
	// server that predates one is not asked again on every query.
	annRejectedMu sync.Mutex
	annRejected   map[string]bool
	// bankCreateHook, when set, runs once after a bank row is newly created.
	bankCreateHook BankCreateHook
	// phaseObserver, when set, receives one observation per phase of a recall
	// the diagnostic accounting sampled, and phaseSampler draws those samples.
	phaseObserver RecallPhaseObserver
	phaseSampler  phaseSampler
}

// SetTuning installs the resolved retrieval, write and reflection tuning. The
// composition root calls it once at startup; a store built without it keeps the
// reference's defaults.
func (s *PostgresStore) SetTuning(tuning Tuning) {
	s.tuning = tuning
	s.recallGate = newRecallGate(tuning.MaxConcurrent)
}

// SetObservationEmbedder gives the store the generator the consolidation write
// path embeds observations with. The composition root calls it once at startup;
// a deployment with no embedding model passes nil and observations stay
// unembedded, exactly as the retain path leaves facts when no model is
// configured.
func (s *PostgresStore) SetObservationEmbedder(generator EmbeddingGenerator) {
	s.observationEmbedder = generator
}

// SetAutoConsolidation installs the deployment default for whether a curation
// edit submits the follow-up consolidation pass. The composition root calls it
// once at startup; a bank's own enable_auto_consolidation override still wins.
func (s *PostgresStore) SetAutoConsolidation(enabled bool) {
	s.autoConsolidation = enabled
}

// SetRecallPhaseObserver installs the receiver of the per-phase recall
// observations. The composition root calls it once at startup; a deployment
// with no metrics registry passes nil and the accounting still logs its
// `[phases]` line.
func (s *PostgresStore) SetRecallPhaseObserver(observer RecallPhaseObserver) {
	s.phaseObserver = observer
}

// beginRecallPhases starts this recall's diagnostic accounting, resolving the
// sample draw from the store's own counter.
func (s *PostgresStore) beginRecallPhases(bankID string) *recallPhases {
	return beginRecallPhases(s.tuning, &s.phaseSampler, s.phaseObserver, bankID)
}

// ObjectStore persists opaque payloads outside the database.
//
// It is declared here rather than importing an implementation so the storage
// area owns the shape it needs while the object-store package owns the
// protocol.
type ObjectStore interface {
	// Put stores the payload under the key, replacing any existing object.
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// Get returns the payload stored under the key, or ErrObjectNotFound.
	Get(ctx context.Context, key string) ([]byte, string, error)
}

// ErrObjectNotFound is the object-store miss, translated into the storage
// area's own sentinel so a caller sees ErrStoredFileNotFound either way.
var ErrObjectNotFound = errors.New("object not found")

// WithObjectStore returns the store with an external payload backend
// attached, so attachment and archive bytes live in the object store instead
// of the database.
func (s *PostgresStore) WithObjectStore(objectStore ObjectStore) *PostgresStore {
	s.objectStore = objectStore
	return s
}

// UsesObjectStore reports whether payloads are kept outside the database.
func (s *PostgresStore) UsesObjectStore() bool { return s.objectStore != nil }

// Schema returns the configured schema target.
func (s *PostgresStore) Schema() string { return s.schema }

// reader is the handle read-only statements run over: the configured read
// replica when one is set, otherwise the primary.
//
// It is never used for a write. The reference keeps its read backend on a pool
// a deployment may point at a read-only role, so a write issued there would be
// refused by the server rather than merely slower; the split is what keeps that
// from happening.
//
// It is used by the paths whose answer is a read of committed state: recall
// (every arm and its hydration), the read surface (documents, chunks, tags,
// entities, graphs), the observability reads, and the list/get reads of banks,
// memories, mental models, knowledge, directives, webhooks, attachments,
// aliases, archived memories, operations and reflect retrieval.
//
// Three kinds of statement stay on the primary, because a replica's lag would
// change the outcome rather than the latency:
//   - writes, migrations and advisory-locked worker and maintenance work;
//   - a read that gates a write (an operation's status before its transition,
//     an alias before it is added, a name before it is resolved);
//   - a read of a row this process wrote moments ago (a stored payload).
func (s *PostgresStore) reader() DB {
	if s.readDB != nil {
		return s.readDB
	}
	return s.db
}

// Close releases the database handles.
func (s *PostgresStore) Close() error {
	if s.migrationDB != nil {
		// The migration handle is this store's own, so closing the store closes
		// both pools rather than leaking the second one.
		_ = s.migrationDB.Close()
	}
	if s.readDB != nil {
		// The read handle is this store's own too, so it is closed with it.
		_ = s.readDB.Close()
	}
	return s.db.Close()
}

// Ready reports database reachability for readiness probing.
func (s *PostgresStore) Ready(ctx context.Context) error {
	return s.db.Ping(ctx)
}

// PoolStats reports the connection pool's utilization for the readiness
// payload. It is best-effort: a database handle that cannot report it returns
// false, and the readiness body then omits the pool keys, as the reference
// does.
func (s *PostgresStore) PoolStats() (PoolStats, bool) {
	provider, ok := s.db.(interface{ PoolStats() PoolStats })
	if !ok {
		return PoolStats{}, false
	}
	return provider.PoolStats(), true
}

// Ping satisfies the monitoring dependency contract.
func (s *PostgresStore) Ping(ctx context.Context) error { return s.Ready(ctx) }

// Migrate brings the target database to the schema the series describes.
//
// The database is first recognised for what it already is. A database the
// reference service migrated itself holds the reference's schema but records
// its state in the reference's own bookkeeping, which this build does not read
// — so before anything runs, the catalogue is compared against the objects the
// series creates, and a database already carrying all of them is adopted: its
// migrations are recorded and no object is created.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	return s.migrateTarget().migrate(ctx, migrateOptions{})
}

// migrateTarget is the store whose handle runs the migration series: this store,
// or a sibling bound to the configured migration database.
//
// The sibling is constructed rather than copied: PostgresStore carries a mutex
// and two stores must not share one, so a struct copy is both a `go vet`
// violation and shared state. It carries exactly what the migration path reads
// — the handle, the schema and the vector backend its reconciliation names.
func (s *PostgresStore) migrateTarget() *PostgresStore {
	if s.migrationDB == nil {
		return s
	}
	return &PostgresStore{db: s.migrationDB, schema: s.schema, vectorExtension: s.vectorExtension}
}

// MigrateWithoutExtensions runs the series without installing or reconciling the
// extensions it depends on, for a deployment that installs them out of band.
//
// A schema that is missing an extension then fails on the first object that
// needs it, naming the type it could not resolve — which is the actionable
// error an operator who asked to skip the step wants.
func (s *PostgresStore) MigrateWithoutExtensions(ctx context.Context) error {
	return s.migrateTarget().migrate(ctx, migrateOptions{skipExtensions: true})
}

// migrateOptions tunes one migration run.
type migrateOptions struct {
	// skipExtensions skips installing the extensions the series depends on.
	skipExtensions bool
}

func (s *PostgresStore) migrate(ctx context.Context, opts migrateOptions) error {
	series, err := MigrationSeries()
	if err != nil {
		return err
	}
	expected, err := ExpectedSchemaOf()
	if err != nil {
		return err
	}
	// The extensions are installed before the series, each in its own
	// transaction holding the extension lock, because the objects the series
	// creates need the types and operator classes they provide. They are
	// installed before the adoption decision rather than after it so that an
	// adopted database gets them too: its objects depend on them just as much,
	// and installing an extension that is already there is a no-op.
	if !opts.skipExtensions {
		if err := s.ensureExtension(ctx, "vector"); err != nil {
			return err
		}
		// The trigram extension backs fuzzy entity-name resolution. A deployment
		// that cannot install it still serves: the resolver falls back to exact
		// matching when the probe fails.
		if err := s.ensureExtension(ctx, "pg_trgm"); err != nil {
			return err
		}
		// btree_gin provides the operator classes the reference's multi-column GIN
		// indexes use, so it is as required as the vector type.
		if err := s.ensureExtension(ctx, "btree_gin"); err != nil {
			return err
		}
	}

	// One transaction carries the whole decision and the whole series. The
	// advisory lock inside it serializes the processes of a deployment: two
	// coming up together would otherwise both find the series unrecorded, both
	// read the same catalogue, and both create its objects, and one would fail
	// on an object that already exists.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('wenyuan:migrations'))`); err != nil {
		return err
	}
	// The schema is created inside the lock so two processes cannot race on its
	// creation either.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("CREATE SCHEMA IF NOT EXISTS %s", quoteIdent(s.schema))); err != nil {
		return err
	}
	// The extension schema stays on the path: a statement in this store may
	// name a type or an operator class the extension provides.
	if _, err := tx.ExecContext(ctx, "SET LOCAL search_path TO "+s.searchPath()); err != nil {
		return err
	}
	if err := s.ensureMigrationTableTx(ctx, tx); err != nil {
		return err
	}
	applied, err := s.appliedMigrationsTx(ctx, tx)
	if err != nil {
		return err
	}
	if len(applied) < len(series) {
		if err := s.reconcileSeries(ctx, tx, series, applied, expected); err != nil {
			return err
		}
	}
	// The stored vector width is a deployment setting rather than part of the
	// schema the series renders, so it is reconciled here against what this
	// service's provider boundary declares - whether the schema was just created
	// or adopted. See reconcileEmbeddingColumns.
	if err := s.reconcileEmbeddingColumns(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// reconcileSeries decides what a series that is not fully recorded means for
// this database: run it, adopt it, or refuse it.
//
// The three outcomes are the only safe ones. Running the series against a
// database that already carries its objects fails on the first one; recording
// it against a database that carries only some of them would skip the
// migrations that built the rest; so the catalogue has to be read and the
// decision made from it before either happens.
func (s *PostgresStore) reconcileSeries(ctx context.Context, tx Tx, series []Migration, applied map[int]bool, expected *ExpectedSchema) error {
	snapshot, err := s.readCatalogue(ctx, tx)
	if err != nil {
		return err
	}
	state, missing := classifySchema(expected, snapshot)
	switch state {
	case schemaComplete:
		// The database is already the schema this build serves, whoever built
		// it. The ledger is brought up to date and nothing is created.
		return s.adoptSeries(ctx, tx, series, applied, expected, snapshot)
	case schemaPartial:
		return s.partialSchemaError(missing, expected, snapshot)
	}
	for _, migration := range series {
		if applied[migration.Version] {
			// A migration already recorded is not run again, so a start is
			// not a full replay and a destructive statement cannot repeat.
			continue
		}
		if err := s.applyMigrationTx(ctx, tx, migration); err != nil {
			return err
		}
	}
	return nil
}

// extensionSchema is the schema the extensions are installed in.
//
// The reference keeps vector, pg_trgm and btree_gin in `public`, and keeping
// them there is what lets a connection resolve them without any session
// setup: the search path is per connection, and a pool hands out connections
// that never saw a SET. Objects in this store's own schema are addressed by
// name, so only the extension types and operator classes have to be reachable
// by an unqualified name.
const extensionSchema = "public"

// searchPath is the path every statement of this store resolves against: its
// own schema first, then the extensions.
func (s *PostgresStore) searchPath() string {
	return quoteIdent(s.schema) + ", " + quoteIdent(extensionSchema)
}

// ensureMigrationTableTx creates the record of applied migrations.
//
// Without it every start replays the whole series, which is both wasted work
// and a hazard: a migration is only idempotent while someone remembers to write
// it that way.
func (s *PostgresStore) ensureMigrationTableTx(ctx context.Context, tx Tx) error {
	query := `CREATE TABLE IF NOT EXISTS ` + quoteIdent(s.schema) + `.schema_migrations (
	              version     INTEGER PRIMARY KEY,
	              name        TEXT NOT NULL,
	              applied_at  TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now()
	          )`
	_, err := tx.ExecContext(ctx, query)
	return err
}

// appliedMigrationsTx returns the versions recorded in the ledger the queryer
// sees, which inside Migrate is the transaction holding the migration lock.
func (s *PostgresStore) appliedMigrationsTx(ctx context.Context, q queryer) (map[int]bool, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT version FROM `+quoteIdent(s.schema)+`.schema_migrations`)
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

// applyMigrationTx runs one migration's statements and records it on the
// caller's transaction, whose search path the caller has already set.
//
// The statements share that transaction, so a migration that fails halfway
// leaves no partial schema behind and is not recorded as applied.
func (s *PostgresStore) applyMigrationTx(ctx context.Context, tx Tx, migration Migration) error {
	for _, statement := range migration.Statements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migration %04d failed: %w", migration.Version, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+quoteIdent(s.schema)+`.schema_migrations (version, name) VALUES ($1, $2)
		 ON CONFLICT (version) DO NOTHING`,
		migration.Version, migration.Name); err != nil {
		return fmt.Errorf("recording migration %04d failed: %w", migration.Version, err)
	}
	return nil
}

// ensureExtension installs a named extension if it is missing. An extension
// that is already present anywhere is left where it is.
//
// The creation runs in a transaction holding the extension advisory lock the
// series' own extension statements take. CREATE EXTENSION IF NOT EXISTS is not
// concurrency-safe: every process of a deployment installs its extensions at
// start, so two of them coming up together both pass the existence check and
// one fails on the catalogue's unique index on the extension name.
func (s *PostgresStore) ensureExtension(ctx context.Context, name string) error {
	var installed string
	probe := `SELECT n.nspname FROM pg_extension e
	          JOIN pg_namespace n ON n.oid = e.extnamespace
	          WHERE e.extname = $1`
	err := s.db.QueryRowContext(ctx, probe, name).Scan(&installed)
	switch {
	case err == nil && installed != "":
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('wenyuan:extensions'))`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`CREATE EXTENSION IF NOT EXISTS `+quoteIdent(name)+` SCHEMA `+quoteIdent(extensionSchema)); err != nil {
		return fmt.Errorf("install %s extension: %w", name, err)
	}
	return tx.Commit()
}

// quoteIdent quotes an SQL identifier. Identifiers come from trusted
// configuration, never from request input.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral renders an SQL string literal. It is for the rare statement that
// has to name a constant in its own text rather than through a parameter — the
// claim query's exclusion of a non-executable operation type, for instance,
// where a parameter would renumber the statement's other placeholders.
func quoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
