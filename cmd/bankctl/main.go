// Command bankctl is the administrative bank lifecycle command surface. It
// renames a bank across its bank-scoped rows and exports/imports a whole bank
// as an archive. It is PostgreSQL-oriented; an in-process store is used when no
// database URL is configured so the command remains invocable offline.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/studyzy/wenyuan/internal/bankadmin"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bankctl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bankctl bank rename --source ID --to ID [--dry-run] | bankctl bank archive export|import --archive FILE [--target ID] | bankctl bank migrate (--archive FILE | --source-url URL --source-bank ID) [--bank-id ID] [--replace|--append|--incremental] [--dry-run]")
	}
	if len(args) < 3 || args[0] != "bank" {
		return fmt.Errorf("expected a 'bank rename' or 'bank archive' command")
	}
	ctx := context.Background()
	store, err := openStore(ctx)
	if err != nil {
		return err
	}

	switch args[1] {
	case "rename":
		flags, err := bankadmin.ParseRename(args[2:])
		if err != nil {
			return err
		}
		_, err = bankadmin.RunRename(ctx, store, flags, os.Stdout)
		return err
	case "archive":
		if len(args) < 4 {
			return fmt.Errorf("archive requires an export or import action")
		}
		flags, err := bankadmin.ParseArchive(args[2], args[3:])
		if err != nil {
			return err
		}
		return bankadmin.RunArchive(ctx, store, flags, os.Stdout)
	case "migrate":
		flags, err := bankadmin.ParseMigrate(args[2:])
		if err != nil {
			return err
		}
		_, err = bankadmin.RunMigrate(ctx, store, flags, os.Stdout)
		return err
	default:
		return fmt.Errorf("unknown bank subcommand %q", args[1])
	}
}

// openStore resolves the store the way the server does, falling back to the
// in-process store when no database URL is configured.
func openStore(ctx context.Context) (storage.Store, error) {
	cfg := config.FromEnvironment()
	backendName := storage.BackendMemory
	backendCfg := storage.BackendConfig{
		Schema:        cfg.DatabaseSchema,
		DatabaseURL:   cfg.DatabaseURL,
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
