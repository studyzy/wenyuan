package bankadmin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// backupManifestVersion is the archive format version. A restore refuses any
// other version rather than guessing at a shape it does not know.
const backupManifestVersion = 1

// BackupFlags is the parsed admin backup command surface.
type BackupFlags struct {
	// Output is the archive file to write.
	Output string
}

// ParseBackup parses a `backup --output FILE` command.
func ParseBackup(args []string) (BackupFlags, error) {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags BackupFlags
	fs.StringVar(&flags.Output, "output", "", "archive file to write")
	fs.StringVar(&flags.Output, "o", "", "archive file to write (short)")
	positional, err := parsePositionals(fs, args)
	if err != nil {
		return BackupFlags{}, err
	}
	output, err := resolveRequired(positional, flags.Output, "an output archive path")
	if err != nil {
		return BackupFlags{}, err
	}
	flags.Output = output
	return flags, nil
}

// RestoreFlags is the parsed admin restore command surface.
type RestoreFlags struct {
	// Input is the archive file to read.
	Input string
	// Yes skips the confirmation prompt; restoring replaces every row.
	Yes bool
}

// ParseRestore parses a `restore --input FILE` command.
func ParseRestore(args []string) (RestoreFlags, error) {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags RestoreFlags
	fs.StringVar(&flags.Input, "input", "", "archive file to read")
	fs.StringVar(&flags.Input, "i", "", "archive file to read (short)")
	fs.BoolVar(&flags.Yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&flags.Yes, "y", false, "skip the confirmation prompt (short)")
	positional, err := parsePositionals(fs, args)
	if err != nil {
		return RestoreFlags{}, err
	}
	input, err := resolveRequired(positional, flags.Input, "an input archive path")
	if err != nil {
		return RestoreFlags{}, err
	}
	flags.Input = input
	return flags, nil
}

// backupManifest describes one archive: the tables it holds and the column
// shape each had when it was taken.
type backupManifest struct {
	Version   int                    `json:"version"`
	Schema    string                 `json:"schema"`
	CreatedAt string                 `json:"created_at"`
	Tables    map[string]backupTable `json:"tables"`
}

// backupTable is one table's entry in the manifest.
type backupTable struct {
	Rows    int                   `json:"rows"`
	Columns []storage.AdminColumn `json:"columns"`
}

// adminTableStore resolves the optional capability the backup and restore
// commands need, refusing a backend that cannot provide it.
func adminTableStore(store storage.Store) (storage.AdminTableStore, error) {
	caps, ok := store.(storage.AdminTableStore)
	if !ok {
		return nil, fmt.Errorf("backup and restore require the PostgreSQL backend; the in-memory store has no persistent schema to enumerate")
	}
	return caps, nil
}

// RunBackup writes every persistent table to one archive. It refuses backends
// that cannot enumerate a table set rather than writing a partial backup.
func RunBackup(ctx context.Context, store storage.Store, flags BackupFlags, out io.Writer) error {
	caps, err := adminTableStore(store)
	if err != nil {
		return err
	}
	tables, err := storage.PersistentTables()
	if err != nil {
		return err
	}

	file, err := os.Create(flags.Output)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	archive := zip.NewWriter(file)

	manifest := backupManifest{
		Version:   backupManifestVersion,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Tables:    map[string]backupTable{},
	}
	// The schema name is recorded for the operator's benefit; a store that does
	// not expose one still backs up.
	if named, ok := store.(interface{ Schema() string }); ok {
		manifest.Schema = named.Schema()
	}
	totalRows := 0
	for _, table := range tables {
		columns, err := caps.TableColumns(ctx, table)
		if err != nil {
			return err
		}
		data, err := caps.DumpTable(ctx, table)
		if err != nil {
			return err
		}
		rows, err := jsonRowCount(data)
		if err != nil {
			return err
		}
		if err := writeZipEntry(archive, table+".json", data); err != nil {
			return err
		}
		manifest.Tables[table] = backupTable{Rows: rows, Columns: columns}
		totalRows += rows
		_, _ = fmt.Fprintf(out, "  %s: %d row(s)\n", table, rows)
	}

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeZipEntry(archive, "manifest.json", encoded); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "backed up %d row(s) across %d table(s) to %s\n", totalRows, len(tables), flags.Output)
	return nil
}

// RunRestore loads a backup back, truncating in reverse dependency order and
// loading forward so foreign keys survive. It validates the archive's shape
// against the target before it truncates anything, and refuses a mismatch
// rather than partially loading it.
func RunRestore(ctx context.Context, store storage.Store, flags RestoreFlags, in io.Reader, out io.Writer) error {
	if !flags.Yes {
		if !Confirm(in, out, "This will DELETE all existing data and replace it with the backup. Continue?") {
			return ErrAborted
		}
	}
	caps, err := adminTableStore(store)
	if err != nil {
		return err
	}
	reader, err := zip.OpenReader(flags.Input)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	manifestData, err := readZipEntry(&reader.Reader, "manifest.json")
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.Version != backupManifestVersion {
		return fmt.Errorf("unsupported backup version %d (this binary restores version %d)", manifest.Version, backupManifestVersion)
	}

	order, err := restoreOrder(manifest)
	if err != nil {
		return err
	}

	// Validate every table against the target before the first truncate, so a
	// drifted schema is an actionable error instead of a half-restored database.
	for _, table := range order {
		target, err := caps.TableColumns(ctx, table)
		if err != nil {
			return err
		}
		if err := validateColumns(table, manifest.Tables[table].Columns, target); err != nil {
			return err
		}
	}

	// Clear every table in one statement: PostgreSQL refuses to truncate a table
	// another table still references unless that table is part of the same
	// command, so the set has to travel together however well ordered it is.
	// Loading then runs in dependency order so every reference exists by the
	// time it is inserted.
	if err := caps.TruncateTables(ctx, order); err != nil {
		return err
	}
	totalRows := 0
	for _, table := range order {
		data, err := readZipEntry(&reader.Reader, table+".json")
		if err != nil {
			return fmt.Errorf("read %s: %w", table, err)
		}
		if err := caps.LoadTable(ctx, table, data); err != nil {
			return err
		}
		rows, err := jsonRowCount(data)
		if err != nil {
			return err
		}
		totalRows += rows
		_, _ = fmt.Fprintf(out, "  restored %s: %d row(s)\n", table, rows)
	}
	_, _ = fmt.Fprintf(out, "restored %d row(s) across %d table(s) from %s\n", totalRows, len(order), flags.Input)
	return nil
}

// restoreOrder returns the archive's tables in dependency order, rejecting any
// table the current schema does not know.
func restoreOrder(manifest backupManifest) ([]string, error) {
	known, err := storage.PersistentTables()
	if err != nil {
		return nil, err
	}
	members := map[string]bool{}
	for name := range manifest.Tables {
		members[name] = true
	}
	order := make([]string, 0, len(members))
	for _, name := range known {
		if members[name] {
			order = append(order, name)
			delete(members, name)
		}
	}
	if len(members) > 0 {
		unknown := make([]string, 0, len(members))
		for name := range members {
			unknown = append(unknown, name)
		}
		return nil, fmt.Errorf("backup holds table(s) this schema does not declare: %s", strings.Join(sortedStrings(unknown), ", "))
	}
	return order, nil
}

// validateColumns refuses a table whose backed-up columns no longer match the
// target. A backup column the target has dropped is ignored (its value is
// simply not restored), but a type change is fatal: the values cannot be
// trusted to land in the same shape.
func validateColumns(table string, backup, target []storage.AdminColumn) error {
	types := make(map[string]string, len(target))
	for _, column := range target {
		types[column.Name] = column.Type
	}
	var mismatched []string
	for _, column := range backup {
		actual, ok := types[column.Name]
		if !ok {
			continue
		}
		if actual != column.Type {
			mismatched = append(mismatched, fmt.Sprintf("%s (%s in backup, %s in target)", column.Name, column.Type, actual))
		}
	}
	if len(mismatched) > 0 {
		return fmt.Errorf("backup is incompatible with target table %q: %s", table, strings.Join(mismatched, ", "))
	}
	return nil
}

// jsonRowCount reports how many rows a dumped table holds.
func jsonRowCount(data []byte) (int, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return 0, fmt.Errorf("parse table rows: %w", err)
	}
	return len(rows), nil
}

// writeZipEntry appends one member to the archive.
func writeZipEntry(archive *zip.Writer, name string, data []byte) error {
	writer, err := archive.Create(name)
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

// readZipEntry returns the bytes of one archive member.
func readZipEntry(reader *zip.Reader, name string) ([]byte, error) {
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = opened.Close() }()
		return io.ReadAll(opened)
	}
	return nil, fmt.Errorf("archive has no %s", name)
}

// sortedStrings returns a copy of names in ascending order.
func sortedStrings(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}
