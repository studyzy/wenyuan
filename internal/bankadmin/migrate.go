package bankadmin

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/migration/hindsight"
	"github.com/studyzy/wenyuan/internal/storage"
)

// MigrateFlags is the parsed Hindsight-migration command surface.
type MigrateFlags struct {
	// Archive is the path to a Hindsight whole-bank export (.zip).
	Archive string
	// BankID is the target bank identifier. Empty uses the archive's source id.
	BankID string
	// Replace deletes the target bank's rows before writing (idempotent).
	Replace bool
	// Append keeps existing rows and adds the archive's rows.
	Append bool
	// DryRun parses and maps without writing.
	DryRun bool
}

// ParseMigrate parses a `bank migrate --from-hindsight` command.
func ParseMigrate(args []string) (MigrateFlags, error) {
	fs := flag.NewFlagSet("bank migrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags MigrateFlags
	fs.StringVar(&flags.Archive, "archive", "", "Hindsight export archive (.zip)")
	fs.StringVar(&flags.Archive, "a", "", "Hindsight export archive (.zip) (short)")
	fs.StringVar(&flags.BankID, "bank-id", "", "target bank identifier")
	fs.BoolVar(&flags.Replace, "replace", false, "clear the target bank before writing (default)")
	fs.BoolVar(&flags.Append, "append", false, "keep existing rows and append")
	fs.BoolVar(&flags.DryRun, "dry-run", false, "parse and report without writing")
	if err := fs.Parse(args); err != nil {
		return MigrateFlags{}, err
	}
	if flags.Archive == "" {
		return MigrateFlags{}, fmt.Errorf("an --archive path is required")
	}
	if flags.Replace && flags.Append {
		return MigrateFlags{}, fmt.Errorf("--replace and --append are mutually exclusive")
	}
	return flags, nil
}

// RunMigrate reads a Hindsight export archive and writes it into the store.
func RunMigrate(ctx context.Context, store storage.Store, flags MigrateFlags, out io.Writer) (hindsight.Report, error) {
	archive, err := hindsightarchive.OpenZip(flags.Archive)
	if err != nil {
		return hindsight.Report{}, err
	}
	mode := hindsight.Replace
	if flags.Append {
		mode = hindsight.Append
	}
	report, err := hindsight.Run(ctx, store, hindsight.Mapping{
		Archive: archive,
		BankID:  flags.BankID,
	}, hindsight.Options{Mode: mode, DryRun: flags.DryRun})
	if err != nil {
		return hindsight.Report{}, err
	}

	verb := "migrated"
	if flags.DryRun {
		verb = "would migrate"
	}
	_, _ = fmt.Fprintf(out, "%s %s from %s (%s mode)\n",
		verb, report.BankID, filepath.Base(flags.Archive), mode)
	_, _ = fmt.Fprintf(out, "  documents: %d\n  chunks: %d\n  facts: %d\n  entities: %d\n  links: %d\n",
		report.Documents, report.Chunks, report.Facts, report.Entities, report.Links)
	if len(report.Unmapped) > 0 {
		_, _ = fmt.Fprintf(out, "  archive fields with no target column (preserved in metadata where possible):\n")
		for _, note := range report.Unmapped {
			_, _ = fmt.Fprintf(out, "    - %s\n", note)
		}
	}
	return report, nil
}
