package bankadmin

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transfer"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

func seedStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "old", Name: "Old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(context.TODO(), "old", "nick", true); err != nil {
		t.Fatal(err)
	}
	return store
}

// ts-0024: dry-run reports the plan without changing anything; the real run
// rewrites the identifier while the bank keeps its aliases.
func TestRenameDryRunAndApply(t *testing.T) {
	store := seedStore(t)
	var out bytes.Buffer

	plan, err := RunRename(context.Background(), store, RenameFlags{Source: "old", Target: "new", DryRun: true}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || plan.Applied || plan.Rows != 1 {
		t.Fatalf("dry-run plan = %+v", plan)
	}
	if _, err := store.GetBank(context.TODO(), "old"); err != nil {
		t.Fatal("dry-run must not change anything")
	}

	plan, err = RunRename(context.Background(), store, RenameFlags{Source: "old", Target: "new"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Applied {
		t.Fatalf("real rename must report it was applied: %+v", plan)
	}
	if _, err := store.GetBank(context.TODO(), "new"); err != nil {
		t.Fatalf("the target bank must exist: %v", err)
	}
	if _, err := store.GetBank(context.TODO(), "old"); err != storage.ErrBankNotFound {
		t.Fatalf("the source bank must be gone: %v", err)
	}
	aliases, err := store.ListAliases(context.TODO(), "new")
	if err != nil || len(aliases) != 1 || aliases[0].Alias != "nick" {
		t.Fatalf("the bank must keep its aliases: %+v, err = %v", aliases, err)
	}
}

// A whole-bank export/import round trip carries the bank's memories and its
// profile into a fresh identifier, and refuses a target that already exists.
func TestExportImportBankRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	if _, err := source.CreateBank(ctx, storage.Bank{BankID: "src", Name: "Source"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.PutDocument(ctx, "src", "d1", storage.Document{ID: "d1", OriginalText: "Alice moved to Berlin."}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "u1", BankID: "src", Text: "Alice moved to Berlin", FactType: storage.TypeWorld,
		DocumentID: "d1", Metadata: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.AddAlias(ctx, "src", "nick", false); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "bank.zip")
	var out bytes.Buffer
	size, err := RunExportBank(ctx, source, ExportBankFlags{BankID: "src", Output: path}, &out)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if size == 0 || !strings.Contains(out.String(), "Exported bank \"src\"") {
		t.Fatalf("export reported %d bytes, out = %q", size, out.String())
	}
	// The bytes are a readable archive naming the source bank.
	archive, err := transferarchive.OpenZip(path)
	if err != nil {
		t.Fatalf("the written archive must be readable: %v", err)
	}
	if archive.Manifest.SourceBankID != "src" || len(archive.Documents) != 1 {
		t.Fatalf("archive = %+v", archive.Manifest)
	}

	target := storage.NewMemoryStore("public")
	out.Reset()
	report, err := RunImportBank(ctx, target, nil, ImportBankFlags{Archive: path, TargetBank: "imported"}, &out)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.TargetBankID != "imported" || report.DocumentsImported != 1 || report.FactsImported != 1 {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(out.String(), "Imported bank \"imported\"") {
		t.Fatalf("import out = %q", out.String())
	}
	bank, err := target.GetBank(ctx, "imported")
	if err != nil {
		t.Fatalf("the imported bank must exist: %v", err)
	}
	if bank.Name != "Source" {
		t.Fatalf("import must reproduce the bank profile: %+v", bank)
	}
	// The facts the report counted are really in the target bank.
	stats, err := target.BankStats(ctx, "imported")
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalNodes == 0 || stats.TotalDocuments == 0 {
		t.Fatalf("imported bank holds no memories: %+v", stats)
	}

	// A restore writes a whole bank, so an existing target is refused.
	if _, err := RunImportBank(ctx, target, nil, ImportBankFlags{Archive: path, TargetBank: "imported"}, &bytes.Buffer{}); !errors.Is(err, transfer.ErrTargetBankExists) {
		t.Fatalf("import onto an existing bank = %v, want ErrTargetBankExists", err)
	}
}

// ts-0024: the rename flags parse in both long and short form.
func TestParseRenameFlags(t *testing.T) {
	flags, err := ParseRename([]string{"--source", "a", "--to", "b", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if flags.Source != "a" || flags.Target != "b" || !flags.DryRun {
		t.Fatalf("parsed flags = %+v", flags)
	}
	if _, err := ParseRename([]string{"--source", "a"}); err == nil {
		t.Fatal("a rename without a target must be rejected")
	}
}

// The export/import flag surfaces take the reference's spellings: a bank and an
// output path to export, an archive and an optional target to import.
func TestParseExportImportBankFlags(t *testing.T) {
	export, err := ParseExportBank([]string{"--bank", "b", "--output", "f.zip", "--include-history"})
	if err != nil {
		t.Fatal(err)
	}
	if export.BankID != "b" || export.Output != "f.zip" || !export.IncludeHistory {
		t.Fatalf("export flags = %+v", export)
	}
	short, err := ParseExportBank([]string{"-b", "b", "-o", "f.zip"})
	if err != nil || short.BankID != "b" || short.Output != "f.zip" {
		t.Fatalf("short export flags = %+v, err = %v", short, err)
	}
	for _, missing := range [][]string{{"--output", "f.zip"}, {"--bank", "b"}} {
		if _, err := ParseExportBank(missing); err == nil {
			t.Fatalf("export without %v must be rejected", missing)
		}
	}

	imp, err := ParseImportBank([]string{"-a", "f.zip", "--target-bank", "t"})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Archive != "f.zip" || imp.TargetBank != "t" {
		t.Fatalf("import flags = %+v", imp)
	}
	// The target is optional: the archive's own source bank is the default.
	if _, err := ParseImportBank([]string{"--archive", "f.zip"}); err != nil {
		t.Fatalf("an import without a target must be accepted: %v", err)
	}
	if _, err := ParseImportBank(nil); err == nil {
		t.Fatal("an import without an archive must be rejected")
	}
}

// A rename preserves the bank's data: the memories written under the source
// identifier survive under the target, rather than being recreated empty.
func TestRenamePreservesBankData(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "old", Name: "Old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: "old",
		Items:  []storage.RetainItem{{Content: "hello world"}},
	}); err != nil {
		t.Fatal(err)
	}
	before := store.UnitsForTest("old")
	if len(before) == 0 {
		t.Fatal("the seed write produced no units")
	}

	if _, err := RunRename(ctx, store, RenameFlags{Source: "old", Target: "new"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	after := store.UnitsForTest("new")
	if len(after) != len(before) {
		t.Fatalf("units after rename = %d, want %d", len(after), len(before))
	}
	if _, err := store.GetBank(ctx, "old"); err != storage.ErrBankNotFound {
		t.Fatalf("the source bank must be gone: %v", err)
	}
}

// A rename onto an identifier that already names a bank is refused, so it
// cannot clobber the target.
func TestRenameRejectsOccupiedTarget(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "old", Name: "Old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "new", Name: "New"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RunRename(ctx, store, RenameFlags{Source: "old", Target: "new"}, &bytes.Buffer{}); err != storage.ErrBankExists {
		t.Fatalf("rename onto an existing bank = %v, want ErrBankExists", err)
	}
	if _, err := store.GetBank(ctx, "old"); err != nil {
		t.Fatalf("a refused rename must leave the source intact: %v", err)
	}
}

// repair-bank over a backend with no per-bank vector indexes reports that there
// is nothing to repair rather than failing.
func TestRunRepairWithoutPerBankIndexes(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	flags := RepairFlags{BankID: "b1", VectorIndexMinRows: 100}
	if err := RunRepair(ctx, store, flags, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to repair") {
		t.Fatalf("repair output = %q", out.String())
	}
}

// Exactly one of --bank or --all is required, as the reference requires.
func TestParseRepairRequiresOneTarget(t *testing.T) {
	for _, args := range [][]string{{}, {"--bank", "b", "--all"}} {
		if _, err := ParseRepair(args); err == nil {
			t.Fatalf("ParseRepair(%v) must be rejected", args)
		}
	}
	flags, err := ParseRepair([]string{"--all", "--dry-run"})
	if err != nil || !flags.AllBanks || !flags.DryRun {
		t.Fatalf("parsed = %+v, err = %v", flags, err)
	}
}

// worker-status reports the tasks the fleet is holding, and an empty queue as
// the reference does.
func TestRunWorkerStatusEmptyQueue(t *testing.T) {
	store := storage.NewMemoryStore("public")
	var out bytes.Buffer
	if err := RunWorkerStatus(context.Background(), store, &out); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "No processing tasks found" {
		t.Fatalf("worker-status output = %q", out.String())
	}
}

// worker-status groups processing tasks by the worker holding them.
func TestRunWorkerStatusGroupsByWorker(t *testing.T) {
	store := seedOperationStore(t)
	ctx := context.Background()
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunWorkerStatus(ctx, store, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Processing tasks across 1 worker(s):", "Worker: worker-1 (1 task(s))", "running=", "last_update="} {
		if !strings.Contains(got, want) {
			t.Errorf("worker-status missing %q:\n%s", want, got)
		}
	}
}

// A declined restore prompt aborts before anything is truncated.
func TestRestoreConfirmationDeclinedAborts(t *testing.T) {
	store := seedStore(t)
	var out bytes.Buffer
	err := RunRestore(context.Background(), store, RestoreFlags{Input: filepath.Join(t.TempDir(), "b.zip")},
		strings.NewReader("n\n"), &out)
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if !strings.Contains(out.String(), "This will DELETE all existing data") {
		t.Fatalf("prompt = %q", out.String())
	}
}

// Confirm takes y/yes case-insensitively and treats anything else as a no.
func TestConfirm(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"  YES  \n", true},
		{"\n", false},
		{"n\n", false},
		{"", false},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		if got := Confirm(strings.NewReader(tc.in), &out, "Continue?"); got != tc.want {
			t.Errorf("Confirm(%q) = %v, want %v", tc.in, got, tc.want)
		}
		if !strings.Contains(out.String(), "Continue? [y/N]: ") {
			t.Errorf("prompt = %q", out.String())
		}
	}
}

// Declining the worker prompt releases nothing.
func TestDecommissionConfirmationDeclined(t *testing.T) {
	store := seedOperationStore(t)
	if _, err := store.ClaimNextOperation(context.Background(), "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := RunDecommissionWorker(context.Background(), store,
		DecommissionFlags{Worker: "worker-1"}, strings.NewReader("no\n"), &out); !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if _, err := RunDecommissionWorkers(context.Background(), store,
		DecommissionWorkersFlags{}, strings.NewReader(""), &out); !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	// The claim is still held: nothing was released.
	if _, err := store.ClaimNextOperation(context.Background(), "worker-2", time.Hour); err == nil {
		t.Fatal("declining must not release the claim")
	}
}

// Missing required flags are usage errors (exit 2), not runtime failures.
func TestParseErrorsAreUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		run  func() error
	}{
		{"rename", func() error { _, err := ParseRename(nil); return err }},
		{"export-bank", func() error { _, err := ParseExportBank(nil); return err }},
		{"import-bank", func() error { _, err := ParseImportBank(nil); return err }},
		{"backup", func() error { _, err := ParseBackup(nil); return err }},
		{"restore", func() error { _, err := ParseRestore(nil); return err }},
		{"decommission-worker", func() error { _, err := ParseDecommissionWorker(nil); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var usage *UsageError
			if err := tc.run(); !errors.As(err, &usage) {
				t.Fatalf("err = %v (%T), want a usage error", err, err)
			}
		})
	}
}
