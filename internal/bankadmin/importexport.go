package bankadmin

// `import-export` replays an archive this service's own `export-bank` wrote.
//
// Why it exists: the served import route applies the reference's archive
// format, whose importer carries documents and configuration but not the
// bank's synthesized state, so a knowledge page arrives with nothing behind it
// and an observation's evidence arrives pointing at units the target never
// wrote. A tenant move has to keep both, and the export already carries them;
// this command writes them back.

import (
	"archive/zip"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// ImportExportFlags is the parsed `import-export` command surface.
type ImportExportFlags struct {
	Archive string
	// Tenant is the tenant the copy belongs to, resolved from --tenant by the
	// dispatcher before the store is opened.
	Tenant string
	// TargetBank is the explicit bank id. Empty re-homes the archive's source
	// name under Tenant (coding-agent::wenyuan -> studyzy::wenyuan).
	TargetBank string
	// Mode decides what an existing target bank means: replace clears it and
	// writes the archive, rename writes the archive beside it under a derived
	// id, and merge folds the archive into it. Empty is refused rather than
	// defaulted, so a destructive replay is never the silent outcome of a
	// missing flag.
	Mode ImportExportMode
	// NoEmbeddings writes the bank without vectors, for an operator who needs the
	// rows on disk before the bank is read: a replay embeds every unit it
	// writes, which is one model request batch per unit.
	NoEmbeddings bool
	// Backfill selects the repair pass: embed the target's units that carry no
	// vector, and nothing else. It is the second half of a replay that was run
	// without vectors, and re-running it is a no-op once every unit has one.
	Backfill bool
}

// ParseImportExport parses `import-export --archive FILE [--target-bank ID]`.
func ParseImportExport(args []string) (ImportExportFlags, error) {
	fs := flag.NewFlagSet("import-export", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags ImportExportFlags
	fs.StringVar(&flags.Archive, "archive", "", "path to the .zip produced by export-bank")
	fs.StringVar(&flags.Archive, "a", "", "path to the .zip (short)")
	fs.StringVar(&flags.TargetBank, "target-bank", "", "override the bank id (defaults to the archive's source name under the tenant)")
	fs.StringVar(&flags.Mode, "mode", "", "what an existing target bank means: rename, replace or merge (required)")
	fs.BoolVar(&flags.Backfill, "backfill", false, "embed the target's units that have no vector, instead of importing")
	if err := fs.Parse(args); err != nil {
		return ImportExportFlags{}, err
	}
	if flags.Archive == "" && !flags.Backfill {
		return ImportExportFlags{}, NewUsageError("an --archive path is required")
	}
	if flags.Backfill {
		return flags, nil
	}
	switch flags.Mode {
	case ImportExportRename, ImportExportReplace, ImportExportMerge:
	default:
		return ImportExportFlags{}, NewUsageError("--mode is required: rename, replace or merge")
	}
	return flags, nil
}

// RunImportExport replays an export archive into the tenant's bank.
func RunImportExport(ctx context.Context, store storage.Store, embeddings storage.EmbeddingGenerator, flags ImportExportFlags, out io.Writer) (ExportImportReport, error) {
	var report ExportImportReport
	if flags.Backfill {
		return report, runBackfill(ctx, store, embeddings, flags, out)
	}
	dir, cleanup, err := unpackArchive(flags.Archive)
	if err != nil {
		return report, err
	}
	defer cleanup()
	archive, err := ReadExportArchive(dir)
	if err != nil {
		return report, err
	}
	target, err := ImportTargetBankID(archive.Manifest.SourceBankID, flags.Tenant, flags.TargetBank)
	if err != nil {
		return report, err
	}
	if flags.NoEmbeddings {
		embeddings = nil
	}
	report, err = RunExportImport(ctx, store, archive, ExportImportOptions{BankID: target, Embeddings: embeddings, Mode: flags.Mode})
	if err != nil {
		return report, err
	}
	tenantClause := ""
	if flags.Tenant != "" {
		tenantClause = fmt.Sprintf(" into tenant %q", flags.Tenant)
	}
	_, _ = fmt.Fprintf(out, "Imported bank %q%s (%s mode): %d doc(s), %d chunk(s), %d fact(s), %d observation(s), "+
		"%d mental model(s), %d knowledge page(s), %d observation(s) lost\n",
		report.BankID, tenantClause, flags.Mode, report.DocumentsImported, report.ChunksImported, report.FactsImported,
		report.ObservationsImported, report.MentalModelsImported, report.KnowledgePagesImported,
		report.ObservationsLost)
	for _, reason := range report.UnwrittenMentalModels {
		_, _ = fmt.Fprintf(out, "  warning: mental model not written: %s\n", reason)
	}
	return report, nil
}

// unpackArchive extracts an archive's zip into a scratch directory, so the
// reader sees the sections as files. Entries that would escape the scratch
// directory are refused rather than written.
func unpackArchive(path string) (string, func(), error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "wenyuan-import-*")
	if err != nil {
		_ = reader.Close()
		return "", nil, err
	}
	cleanup := func() {
		_ = reader.Close()
		_ = os.RemoveAll(dir)
	}
	for _, entry := range reader.File {
		target := filepath.Join(dir, filepath.Clean(string(filepath.Separator)+entry.Name))
		if !strings.HasPrefix(target, dir+string(filepath.Separator)) {
			cleanup()
			return "", nil, fmt.Errorf("archive entry %q escapes the archive", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			cleanup()
			return "", nil, err
		}
		if err := writeArchiveEntry(entry, target); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}

// writeArchiveEntry copies one archive entry out to target.
func writeArchiveEntry(entry *zip.File, target string) error {
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	destination, err := os.Create(target)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		return err
	}
	return destination.Close()
}
