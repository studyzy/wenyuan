package bankadmin

// Whole-bank export and import, the admin surface's cross-instance transfer.
//
// The archive is the same format the served transfer routes use, so a bank
// exported here imports there and in the reference, and an archive the
// reference exported imports here. Both commands drive the transfer engine
// directly rather than going through a served route, which is what lets an
// operator move a bank between instances without a running API.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transfer"
)

// ExportBankFlags is the parsed `export-bank` command surface: which bank, where
// the archive goes, and whether the operational history travels with it.
type ExportBankFlags struct {
	BankID         string
	Output         string
	IncludeHistory bool
}

// ParseExportBank parses `export-bank --bank ID --output FILE [--include-history]`.
func ParseExportBank(args []string) (ExportBankFlags, error) {
	fs := flag.NewFlagSet("export-bank", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags ExportBankFlags
	fs.StringVar(&flags.BankID, "bank", "", "bank id to export")
	fs.StringVar(&flags.BankID, "b", "", "bank id to export (short)")
	fs.StringVar(&flags.Output, "output", "", "path to write the .zip archive")
	fs.StringVar(&flags.Output, "o", "", "path to write the .zip archive (short)")
	fs.BoolVar(&flags.IncludeHistory, "include-history", false, "also carry operational history (audit log, llm requests)")
	if err := fs.Parse(args); err != nil {
		return ExportBankFlags{}, err
	}
	if flags.BankID == "" {
		return ExportBankFlags{}, NewUsageError("a --bank identifier is required")
	}
	if flags.Output == "" {
		return ExportBankFlags{}, NewUsageError("an --output path is required")
	}
	return flags, nil
}

// ImportBankFlags is the parsed `import-bank` command surface.
type ImportBankFlags struct {
	Archive        string
	TargetBank     string
	IncludeHistory bool
}

// ParseImportBank parses `import-bank --archive FILE [--target-bank ID] [--include-history]`.
func ParseImportBank(args []string) (ImportBankFlags, error) {
	fs := flag.NewFlagSet("import-bank", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags ImportBankFlags
	fs.StringVar(&flags.Archive, "archive", "", "path to the .zip produced by export-bank")
	fs.StringVar(&flags.Archive, "a", "", "path to the .zip (short)")
	fs.StringVar(&flags.TargetBank, "target-bank", "", "override the bank id (defaults to the archive's source bank)")
	fs.BoolVar(&flags.IncludeHistory, "include-history", false, "also restore operational history if the archive carries it")
	if err := fs.Parse(args); err != nil {
		return ImportBankFlags{}, err
	}
	if flags.Archive == "" {
		return ImportBankFlags{}, NewUsageError("an --archive path is required")
	}
	return flags, nil
}

// BankExportScope is the section selector a whole-bank transfer carries: the
// memories and the bank's configuration, plus the operational history when it
// was asked for. It is the reference's own default scope.
func BankExportScope(includeHistory bool) transfer.ExportScope {
	return transfer.ExportScope{Data: true, BankConfig: true, History: includeHistory}
}

// BankExportOptions translates a whole-bank export into the archive builder's
// options. Observations and the knowledge base are a reading of the bank's own
// facts, so they ride with the data section.
func BankExportOptions(includeHistory bool) transfer.ExportOptions {
	return transfer.ExportOptions{
		IncludeObservations:  true,
		IncludeKnowledgeBase: true,
		Scope:                BankExportScope(includeHistory),
	}
}

// RunExportBank builds a whole-bank archive and writes it to flags.Output. The
// bytes land through a temporary file beside the destination and are renamed
// into place, so an interrupted export never leaves a truncated archive where a
// complete one is expected.
func RunExportBank(ctx context.Context, store storage.Store, flags ExportBankFlags, out io.Writer) (int, error) {
	archive, err := transfer.BuildArchive(ctx, store, flags.BankID, BankExportOptions(flags.IncludeHistory))
	if err != nil {
		return 0, err
	}
	data, err := hindsightarchive.Write(archive)
	if err != nil {
		return 0, err
	}
	if err := writeFileAtomic(flags.Output, data); err != nil {
		return 0, err
	}
	_, _ = fmt.Fprintf(out, "Exported bank %q to %s (%d bytes)\n", flags.BankID, flags.Output, len(data))
	return len(data), nil
}

// RunImportBank restores a whole bank from an archive. A restore writes a whole
// bank, so the target must not already exist; the import reports what it wrote
// in the same categories the reference does.
func RunImportBank(ctx context.Context, store storage.Store, embeddings storage.EmbeddingGenerator, flags ImportBankFlags, out io.Writer) (transfer.ImportReport, error) {
	data, err := os.ReadFile(flags.Archive)
	if err != nil {
		return transfer.ImportReport{}, err
	}
	archive, err := transfer.ParseArchive(data)
	if err != nil {
		return transfer.ImportReport{}, err
	}
	report, err := transfer.ImportApplier{Store: store, Embeddings: embeddings}.Apply(ctx, transfer.ImportRequest{
		Archive:      archive,
		BankID:       archive.Manifest.SourceBankID,
		TargetBankID: flags.TargetBank,
		Mode:         transfer.ImportRestore,
		Scope:        BankExportScope(flags.IncludeHistory),
	})
	if err != nil {
		return report, err
	}
	_, _ = fmt.Fprintf(out, "Imported bank %q: %d doc(s), %d fact(s), %d observation(s), "+
		"%d attachment(s), %d mental model(s), %d mm-history row(s), %d knowledge page(s), "+
		"%d directive(s), %d webhook(s), %d history row(s), %d operation(s), %d invalidated fact(s)\n",
		report.TargetBankID, report.DocumentsImported, report.FactsImported, report.ObservationsImported,
		report.AttachmentsImported, report.MentalModelsImported, report.MentalModelHistory,
		report.KnowledgePagesImported, report.DirectivesImported, report.WebhooksImported,
		report.HistoryRows, report.Operations, report.InvalidatedMemories)
	return report, nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, so a failure leaves the destination untouched rather than half
// written.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// The temporary file carries the process's private mode; the archive itself
	// takes the ordinary file mode, as the backup command's and the reference's
	// exports do.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
