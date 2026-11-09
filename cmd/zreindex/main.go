// Command zreindex rebuilds the retrieval columns of banks that have none.
//
// An archive carries no vectors: importing one leaves every row's embedding and
// search_vector empty, so recall answers nothing from the semantic or keyword
// arm until they are rebuilt. This tool is the repair step.
//
// The schema and the bank ids come from the command line, because the tool
// operates on one tenant's schema that the caller names:
//
//	zreindex -schema acme 'acme::web'
//
// It needs an embedding backend: a row left unembedded would look repaired while
// the semantic arm still could not rank it, so a run without one is refused.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/studyzy/wenyuan/internal/app"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

func main() {
	ctx := context.Background()
	if err := config.LoadDotenvForEntrypoint(); err != nil {
		fmt.Println("load config:", err)
		os.Exit(1)
	}
	cfg := config.FromEnvironment()
	schema := cfg.DatabaseSchema
	flag.StringVar(&schema, "schema", cfg.DatabaseSchema, "tenant schema holding the banks (default: WENYUAN_API_DATABASE_SCHEMA)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Println("usage: zreindex [-schema <schema>] <bank-id>...")
		os.Exit(2)
	}
	embedder, err := app.ResolveEmbedder(cfg)
	if err != nil || embedder == nil {
		fmt.Println("no embedding backend:", err)
		os.Exit(1)
	}
	gen := app.EmbeddingGeneratorFrom(embedder)
	backend, _ := storage.SelectBackend(storage.BackendPostgreSQL)
	store, err := backend.Open(ctx, storage.BackendConfig{Schema: schema, DatabaseURL: cfg.DatabaseURL})
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	pg := store.(*storage.PostgresStore)
	sctx, err := storage.WithSchema(ctx, schema)
	if err != nil {
		fmt.Println("schema:", err)
		os.Exit(1)
	}
	start := time.Now()
	total, err := pg.BackfillVectorsForBanks(sctx, flag.Args(), gen, os.Stdout)
	fmt.Printf("total %d rows in %s, err=%v\n", total, time.Since(start), err)
	if err != nil {
		os.Exit(1)
	}
}
