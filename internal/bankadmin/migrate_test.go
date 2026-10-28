package bankadmin

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// writeMigrateFixture builds a minimal export archive on disk and returns its
// path.
func writeMigrateFixture(t *testing.T, manifest string, documents map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bank.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	zw := zip.NewWriter(file)
	write := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write(transferarchive.ManifestName, manifest)
	for name, body := range documents {
		write(transferarchive.DocumentsDir+"/"+name, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// migrateFixture builds one archive carrying a single document with one fact.
func migrateFixture(t *testing.T) string {
	t.Helper()
	return writeMigrateFixture(t,
		`{"schema_version":1,"source_bank_id":"src-bank","document_count":1,"fact_count":1,"archive_type":"bank"}`,
		map[string]string{
			"000000.json": `{"id":"doc-a","original_text":"alpha",
			  "chunks":[{"chunk_index":0,"chunk_text":"alpha"}],
			  "facts":[{"text":"Alice works at Google","fact_type":"world","entities":["Alice"]}]}`,
		})
}

// The migrate command reads an archive and writes it into the store, reporting
// its counts.
func TestRunMigrateFromArchive(t *testing.T) {
	store := storage.NewMemoryStore("public")
	var out bytes.Buffer
	report, err := RunMigrate(context.Background(), store, MigrateFlags{Archive: migrateFixture(t)}, &out)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if report.Facts != 1 || report.Documents != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(out.String(), "migrated") || !strings.Contains(out.String(), "facts: 1") {
		t.Fatalf("output = %q", out.String())
	}
	if _, err := store.GetBank(context.Background(), "src-bank"); err != nil {
		t.Fatalf("the target bank was not created: %v", err)
	}
}

// A dry run reports without writing.
func TestRunMigrateDryRun(t *testing.T) {
	store := storage.NewMemoryStore("public")
	var out bytes.Buffer
	if _, err := RunMigrate(context.Background(), store, MigrateFlags{Archive: migrateFixture(t), DryRun: true}, &out); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out.String(), "would migrate") {
		t.Fatalf("output = %q", out.String())
	}
	if _, err := store.GetBank(context.Background(), "src-bank"); err == nil {
		t.Fatal("a dry run must not create the bank")
	}
}

// An incremental migrate writes only what the target does not already hold and
// reports the split.
func TestRunMigrateIncremental(t *testing.T) {
	store := storage.NewMemoryStore("public")
	archive := migrateFixture(t)
	if _, err := RunMigrate(context.Background(), store, MigrateFlags{Archive: archive, Incremental: true}, &bytes.Buffer{}); err != nil {
		t.Fatalf("first incremental: %v", err)
	}
	var out bytes.Buffer
	report, err := RunMigrate(context.Background(), store, MigrateFlags{Archive: archive, Incremental: true}, &out)
	if err != nil {
		t.Fatalf("second incremental: %v", err)
	}
	if report.Written != 0 || report.Skipped != 1 {
		t.Fatalf("second run = written %d skipped %d", report.Written, report.Skipped)
	}
	if !strings.Contains(out.String(), "skipped (already present): 1") {
		t.Fatalf("output = %q", out.String())
	}
}

// A migrate that targets an explicit bank writes under that identifier.
func TestRunMigrateExplicitTarget(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := RunMigrate(context.Background(), store, MigrateFlags{Archive: migrateFixture(t), BankID: "dst"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := store.GetBank(context.Background(), "dst"); err != nil {
		t.Fatalf("the named target bank was not created: %v", err)
	}
}

// The migrate flag parser enforces its required inputs and its mutual
// exclusions.
func TestParseMigrateFlags(t *testing.T) {
	if _, err := ParseMigrate(nil); err == nil {
		t.Fatal("an archive or source URL is required")
	}
	if _, err := ParseMigrate([]string{"--archive", "a.zip", "--replace", "--append"}); err == nil {
		t.Fatal("replace and append are mutually exclusive")
	}
	if _, err := ParseMigrate([]string{"--archive", "a.zip", "--incremental", "--append"}); err == nil {
		t.Fatal("incremental and append are mutually exclusive")
	}
	if _, err := ParseMigrate([]string{"--archive", "a.zip", "--source-url", "postgres://x"}); err == nil {
		t.Fatal("archive and source-url are mutually exclusive")
	}
	flags, err := ParseMigrate([]string{"--archive", "a.zip", "--incremental", "--bank-id", "b1"})
	if err != nil {
		t.Fatalf("valid flags: %v", err)
	}
	if !flags.Incremental || flags.BankID != "b1" || flags.Archive != "a.zip" {
		t.Fatalf("parsed flags = %+v", flags)
	}
}

// Reading a bank straight out of a reference-shaped database requires a driver
// and a source bank, and reports each missing input.
func TestReadDirectBankValidation(t *testing.T) {
	if _, closeFn, err := readDirectBank(context.Background(), MigrateFlags{}); err == nil {
		closeFn()
		t.Fatal("a source bank is required")
	}
}
