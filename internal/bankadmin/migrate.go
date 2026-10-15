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
	// SourceURL is a database URL of a reference-shaped instance to read the
	// bank straight out of, as an alternative to an exported archive.
	SourceURL string
	// SourceBank is the bank to read when SourceURL is set. Empty falls back
	// to the target bank id.
	SourceBank string
	// BankID is the target bank identifier. Empty uses the archive's source id.
	BankID string
	// Replace deletes the target bank's rows before writing (idempotent).
	Replace bool
	// Append keeps existing rows and adds the archive's rows.
	Append bool
	// Incremental keeps existing rows and writes only documents the target does
	// not already hold, so the command can be re-run after further source
	// writes without duplicating or destroying what was already moved.
	Incremental bool
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
	fs.StringVar(&flags.SourceURL, "source-url", "", "database URL of a reference-shaped instance to read directly")
	fs.StringVar(&flags.SourceBank, "source-bank", "", "bank to read when --source-url is set")
	fs.StringVar(&flags.BankID, "bank-id", "", "target bank identifier")
	fs.BoolVar(&flags.Replace, "replace", false, "clear the target bank before writing (default)")
	fs.BoolVar(&flags.Append, "append", false, "keep existing rows and append")
	fs.BoolVar(&flags.Incremental, "incremental", false, "write only documents the target does not already hold")
	fs.BoolVar(&flags.DryRun, "dry-run", false, "parse and report without writing")
	if err := fs.Parse(args); err != nil {
		return MigrateFlags{}, err
	}
	if flags.Archive == "" && flags.SourceURL == "" {
		return MigrateFlags{}, NewUsageError("an --archive path or a --source-url is required")
	}
	if flags.Archive != "" && flags.SourceURL != "" {
		return MigrateFlags{}, NewUsageError("--archive and --source-url are mutually exclusive")
	}
	modes := 0
	for _, set := range []bool{flags.Replace, flags.Append, flags.Incremental} {
		if set {
			modes++
		}
	}
	if modes > 1 {
		return MigrateFlags{}, NewUsageError("--replace, --append and --incremental are mutually exclusive")
	}
	return flags, nil
}

// readDirectBank reads a bank straight out of a reference-shaped database. It
// returns the archive and a close function the caller must run.
func readDirectBank(ctx context.Context, flags MigrateFlags) (*hindsightarchive.Archive, func(), error) {
	if !storage.DriverAvailable() {
		return nil, func() {}, fmt.Errorf("a database driver is required to read --source-url; build with the pgx tag")
	}
	db, err := storage.OpenDatabase(flags.SourceURL)
	if err != nil {
		return nil, func() {}, err
	}
	bankID := flags.SourceBank
	if bankID == "" {
		bankID = flags.BankID
	}
	if bankID == "" {
		_ = db.Close()
		return nil, func() {}, fmt.Errorf("--source-bank (or --bank-id) is required to read a bank directly")
	}
	archive, err := storage.NewDirectSource(db, bankID).Read(ctx)
	if err != nil {
		_ = db.Close()
		return nil, func() {}, err
	}
	return archive, func() { _ = db.Close() }, nil
}

// RunMigrate reads a Hindsight bank — from an export archive or straight out of
// a reference-shaped database — and writes it into the store.
func RunMigrate(ctx context.Context, store storage.Store, flags MigrateFlags, out io.Writer) (hindsight.Report, error) {
	var (
		archive *hindsightarchive.Archive
		source  string
	)
	if flags.SourceURL != "" {
		read, closeSource, err := readDirectBank(ctx, flags)
		if err != nil {
			return hindsight.Report{}, err
		}
		defer closeSource()
		archive = read
		source = "direct read of " + flags.SourceURL
	} else {
		opened, err := hindsightarchive.OpenZip(flags.Archive)
		if err != nil {
			return hindsight.Report{}, err
		}
		archive = opened
		source = filepath.Base(flags.Archive)
	}

	mode := hindsight.Replace
	switch {
	case flags.Append:
		mode = hindsight.Append
	case flags.Incremental:
		mode = hindsight.Incremental
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
	_, _ = fmt.Fprintf(out, "%s %s from %s (%s mode)\n", verb, report.BankID, source, mode)
	_, _ = fmt.Fprintf(out, "  documents: %d\n  chunks: %d\n  facts: %d\n  entities: %d\n  links: %d\n",
		report.Documents, report.Chunks, report.Facts, report.Entities, report.Links)
	if mode == hindsight.Incremental {
		_, _ = fmt.Fprintf(out, "  written: %d\n  skipped (already present): %d\n", report.Written, report.Skipped)
	}
	if len(report.Unmapped) > 0 {
		_, _ = fmt.Fprintf(out, "  archive fields with no target column (preserved in metadata where possible):\n")
		for _, note := range report.Unmapped {
			_, _ = fmt.Fprintf(out, "    - %s\n", note)
		}
	}
	return report, nil
}
