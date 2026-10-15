// Command bankctl is the administrative bank command surface: it renames a
// bank across its bank-scoped rows, exports and imports a whole bank as a
// portable archive, backs the schema up and restores it, and manages the
// worker queue.
//
// It is PostgreSQL-oriented; an in-process store is used when no database URL
// is configured so the command stays invocable offline, and a command that
// needs the persistent schema refuses clearly rather than half-running.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/studyzy/wenyuan/internal/app"
	"github.com/studyzy/wenyuan/internal/bankadmin"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// usage is the one-line synopsis printed when no command is given.
const usage = "usage: bankctl bank " +
	"rename --from OLD --to NEW [--dry-run] | " +
	"export-bank --bank ID --output FILE [--include-history] | " +
	"import-bank --archive FILE [--target-bank ID] [--include-history] | " +
	"backup [--output] FILE | " +
	"restore [--input] FILE [--yes] | " +
	"run-db-migration | " +
	"repair-bank (--bank ID | --all) [--dry-run] | " +
	"worker-status | " +
	"decommission-worker [--worker] ID [--yes] | " +
	"decommission-workers [--yes] | " +
	"migrate (--archive FILE | --source-url URL --source-bank ID) [--bank-id ID] [--replace|--append|--incremental] [--dry-run]"

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "bankctl:", err)
		var usageErr *bankadmin.UsageError
		if errors.As(err, &usageErr) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	if len(args) < 2 || args[0] != "bank" {
		return errors.New(usage)
	}
	ctx := context.Background()
	schema, args, err := splitSchemaOption(args)
	if err != nil {
		return err
	}
	if args[1] == "run-db-migration" {
		return runDBMigration(ctx, schema, args[2:], stdout)
	}
	store, err := openStore(ctx, schema)
	if err != nil {
		return err
	}
	return dispatch(ctx, store, args[1], args[2:], stdin, stdout)
}

// splitSchemaOption lifts the global --schema/-s selector out of the argument
// list. Every database command takes it, as the reference's commands do: it
// decides which schema the store is opened against, so it cannot be a
// per-command flag.
func splitSchemaOption(args []string) (string, []string, error) {
	schema := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--schema" || arg == "-s":
			if i+1 >= len(args) {
				return "", nil, bankadmin.NewUsageError("flag %s needs a value", arg)
			}
			i++
			schema = args[i]
		case strings.HasPrefix(arg, "--schema=") || strings.HasPrefix(arg, "-s="):
			schema = arg[strings.Index(arg, "=")+1:]
		default:
			rest = append(rest, arg)
		}
	}
	return schema, rest, nil
}

// runDBMigration migrates one schema, or the base schema plus every discovered
// tenant schema when no selector was given, matching the reference's sweep.
func runDBMigration(ctx context.Context, schema string, args []string, stdout io.Writer) error {
	flags, err := bankadmin.ParseDBMigration(args)
	if err != nil {
		return err
	}
	cfg := config.FromEnvironment()
	schemas, err := migrationSchemas(ctx, cfg, schema)
	if err != nil {
		return err
	}
	if schema != "" {
		_, _ = fmt.Fprintf(stdout, "Running database migrations for schema: %s...\n", schema)
	} else {
		_, _ = fmt.Fprintln(stdout, "Running database migrations for base schema and all discovered tenant schemas...")
	}
	if flags.SkipExtensionReconcile {
		_, _ = fmt.Fprintln(stdout, "Skipping post-migration extension reconcile (--skip-extension-reconcile).")
	}
	for _, target := range schemas {
		store, err := openStore(ctx, target)
		if err != nil {
			return err
		}
		if err := bankadmin.RunDBMigration(ctx, store, flags, stdout); err != nil {
			return fmt.Errorf("schema %s: %w", target, err)
		}
	}
	_, _ = fmt.Fprintf(stdout, "Database migrations completed successfully for %d schema(s)\n", len(schemas))
	return nil
}

// migrationSchemas resolves the schemas a migration sweep covers: the explicit
// selector when given, otherwise the configured base schema plus every schema
// the tenant extension reports.
func migrationSchemas(ctx context.Context, cfg config.Config, schema string) ([]string, error) {
	if schema != "" {
		return []string{schema}, nil
	}
	listed, err := app.NewTenantExtension(cfg).ListTenants(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	schemas := make([]string, 0, len(listed))
	for _, entry := range listed {
		name := strings.TrimSpace(entry.Schema)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		schemas = append(schemas, name)
	}
	if len(schemas) == 0 {
		schemas = append(schemas, cfg.DatabaseSchema)
	}
	return schemas, nil
}

// dispatch runs one bank subcommand against the store.
func dispatch(ctx context.Context, store storage.Store, command string, args []string, stdin io.Reader, stdout io.Writer) error {
	switch command {
	case "rename":
		flags, err := bankadmin.ParseRename(args)
		if err != nil {
			return err
		}
		_, err = bankadmin.RunRename(ctx, store, flags, stdout)
		return err
	case "export-bank":
		flags, err := bankadmin.ParseExportBank(args)
		if err != nil {
			return err
		}
		_, err = bankadmin.RunExportBank(ctx, store, flags, stdout)
		return err
	case "import-bank":
		flags, err := bankadmin.ParseImportBank(args)
		if err != nil {
			return err
		}
		_, err = bankadmin.RunImportBank(ctx, store, importEmbeddings(stdout), flags, stdout)
		return err
	case "migrate":
		flags, err := bankadmin.ParseMigrate(args)
		if err != nil {
			return err
		}
		_, err = bankadmin.RunMigrate(ctx, store, flags, stdout)
		return err
	case "repair-bank":
		flags, err := bankadmin.ParseRepair(args)
		if err != nil {
			return err
		}
		// The coverage threshold is the deployment's configured value unless the
		// operator overrode it, so a repair does not have to repeat it.
		if flags.VectorIndexMinRows == 0 {
			flags.VectorIndexMinRows = config.FromEnvironment().VectorIndex.MinRows
		}
		return bankadmin.RunRepair(ctx, store, flags, stdout)
	case "worker-status":
		return bankadmin.RunWorkerStatus(ctx, store, stdout)
	case "backup":
		flags, err := bankadmin.ParseBackup(args)
		if err != nil {
			return err
		}
		return bankadmin.RunBackup(ctx, store, flags, stdout)
	case "restore":
		flags, err := bankadmin.ParseRestore(args)
		if err != nil {
			return err
		}
		return bankadmin.RunRestore(ctx, store, flags, stdin, stdout)
	case "decommission-worker":
		flags, err := bankadmin.ParseDecommissionWorker(args)
		if err != nil {
			return err
		}
		_, err = bankadmin.RunDecommissionWorker(ctx, store, flags, stdin, stdout)
		return err
	case "decommission-workers":
		flags, err := bankadmin.ParseDecommissionWorkers(args)
		if err != nil {
			return err
		}
		_, err = bankadmin.RunDecommissionWorkers(ctx, store, flags, stdin, stdout)
		return err
	default:
		return fmt.Errorf("unknown bank subcommand %q", command)
	}
}

// importEmbeddings resolves the deployment's embedding backend for an archive
// import, so an imported fact is embedded with the target's own model exactly
// as a retained one is. A deployment with no embedding provider imports
// unembedded, which is reported rather than left silent: the vector arm then has
// no vectors to rank until facts are embedded by another path.
func importEmbeddings(stdout io.Writer) storage.EmbeddingGenerator {
	embedder, err := app.ResolveEmbedder(config.FromEnvironment())
	if err != nil {
		_, _ = fmt.Fprintf(stdout, "warning: no embedding backend could be resolved (%v); importing without vectors\n", err)
		return nil
	}
	generator := app.EmbeddingGeneratorFrom(embedder)
	if generator == nil {
		_, _ = fmt.Fprintln(stdout, "warning: no embedding backend is configured; importing without vectors")
	}
	return generator
}

// openStore resolves the store the way the server does, falling back to the
// in-process store when no database URL is configured.
func openStore(ctx context.Context, schema string) (storage.Store, error) {
	cfg := config.FromEnvironment()
	if schema == "" {
		schema = cfg.DatabaseSchema
	}
	backendName := storage.BackendMemory
	backendCfg := storage.BackendConfig{
		Schema:        schema,
		DatabaseURL:   cfg.DatabaseURL,
		MaxOpenConns:  cfg.DBPoolMaxSize,
		MaxIdleConns:  cfg.DBPoolMinSize,
		RunMigrations: cfg.RunMigrationsOnStartup,
	}
	if cfg.DatabaseURL != "" {
		backendName = storage.BackendPostgreSQL
	}
	backend, err := storage.SelectBackend(backendName)
	if err != nil {
		return nil, err
	}
	return backend.Open(ctx, backendCfg)
}
