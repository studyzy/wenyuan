// Command schematool creates or drops a wenyuan schema for schema comparison.
//
// It exists so the alignment work is verifiable with one command rather than a
// throwaway program each time: the check is "render both schemas with the same
// query and diff them", and that needs a way to produce this side's schema on
// demand.
//
// It is a development tool, not part of the served surface.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "schematool:", err)
		os.Exit(1)
	}
}

func run() error {
	schema := flag.String("schema", "", "schema to create or drop")
	drop := flag.Bool("drop", false, "drop the schema instead of creating it")
	url := flag.String("url", os.Getenv("WENYUAN_API_DATABASE_URL"), "database URL")
	flag.Parse()

	if *schema == "" {
		return fmt.Errorf("--schema is required")
	}
	if *url == "" {
		return fmt.Errorf("--url or WENYUAN_API_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	backend := &storage.PostgresBackend{}
	// The schema tool gives the pool a small budget: it runs a handful of
	// statements and holding a hundred connections open would only take them
	// from whoever else uses the database.
	store, err := backend.Open(ctx, storage.BackendConfig{
		DatabaseURL: *url, Schema: *schema, RunMigrations: true, MaxOpenConns: 2, MaxIdleConns: 1,
	})
	if err != nil {
		return err
	}
	pg, ok := store.(*storage.PostgresStore)
	if !ok {
		return fmt.Errorf("expected a postgres store, got %T", store)
	}
	defer func() { _ = pg.Close() }()

	if *drop {
		if err := pg.DropSchemaForTest(*schema); err != nil {
			return err
		}
		fmt.Println("dropped", *schema)
		return nil
	}
	if err := pg.Migrate(ctx); err != nil {
		return err
	}
	fmt.Println(*schema)
	return nil
}
