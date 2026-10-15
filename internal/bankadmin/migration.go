package bankadmin

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// DBMigrationFlags is the parsed `run-db-migration` command surface.
type DBMigrationFlags struct {
	// EmbeddingDimension enforces the vector column width the deployment
	// expects. Zero skips the check.
	EmbeddingDimension int
	// SkipExtensionReconcile runs the series without installing or reconciling
	// the extensions it depends on.
	SkipExtensionReconcile bool
}

// ParseDBMigration reads the migration command flags. The schema selector is a
// dispatcher-level option because it decides which store is opened, not what the
// migration does.
func ParseDBMigration(args []string) (DBMigrationFlags, error) {
	fs := flag.NewFlagSet("run-db-migration", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags DBMigrationFlags
	fs.IntVar(&flags.EmbeddingDimension, "embedding-dimension", 0,
		"expected embedding dimension to enforce; omit to skip the check")
	fs.BoolVar(&flags.SkipExtensionReconcile, "skip-extension-reconcile", false,
		"run the series without installing or reconciling the extensions it depends on")
	if err := fs.Parse(args); err != nil {
		return DBMigrationFlags{}, err
	}
	if flags.EmbeddingDimension < 0 {
		return DBMigrationFlags{}, NewUsageError("--embedding-dimension must not be negative")
	}
	return flags, nil
}

// EmbeddingDimensionMismatch reports whether an operator's expected embedding
// dimension disagrees with the width the migration series fixes.
//
// The series declares the vector column width, so the dimension is not a
// migration input here as it is in the reference; the honest check is to refuse
// a value the schema cannot hold rather than to alter the column under a live
// deployment.
func EmbeddingDimensionMismatch(expected int) error {
	if expected == 0 || expected == provider.EmbeddingDimensions {
		return nil
	}
	return fmt.Errorf("--embedding-dimension %d does not match the migration series' vector(%d): "+
		"the schema width is fixed by the series, so re-point the deployment at a provider of that width "+
		"or migrate the column deliberately",
		expected, provider.EmbeddingDimensions)
}

// RunDBMigration applies the store's ordered, idempotent migration series,
// bringing one schema up to the latest version. It is safe to re-run: a
// migration already recorded is not applied again.
//
// The embedding-dimension check runs first, so a deployment pointed at a
// provider the schema cannot hold is told before any object is created.
func RunDBMigration(ctx context.Context, store storage.Store, flags DBMigrationFlags, out io.Writer) error {
	if err := EmbeddingDimensionMismatch(flags.EmbeddingDimension); err != nil {
		return err
	}
	if flags.SkipExtensionReconcile {
		if migrator, ok := store.(storage.SchemaMigrator); ok {
			if err := migrator.MigrateWithoutExtensions(ctx); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(out, "database migrations completed successfully (extensions left as they are)")
			return nil
		}
	}
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "database migrations completed successfully")
	return nil
}
