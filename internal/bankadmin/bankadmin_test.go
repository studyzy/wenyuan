package bankadmin

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

func seedStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(nil, storage.Bank{ID: "old", Name: "Old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAlias(nil, "old", "nick", true); err != nil {
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
	if _, err := store.GetBank(nil, "old"); err != nil {
		t.Fatal("dry-run must not change anything")
	}

	plan, err = RunRename(context.Background(), store, RenameFlags{Source: "old", Target: "new"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Applied {
		t.Fatalf("real rename must report it was applied: %+v", plan)
	}
	if _, err := store.GetBank(nil, "new"); err != nil {
		t.Fatalf("the target bank must exist: %v", err)
	}
	if _, err := store.GetBank(nil, "old"); err != storage.ErrBankNotFound {
		t.Fatalf("the source bank must be gone: %v", err)
	}
	aliases, err := store.ListAliases(nil, "new")
	if err != nil || len(aliases) != 1 || aliases[0].Alias != "nick" {
		t.Fatalf("the bank must keep its aliases: %+v, err = %v", aliases, err)
	}
}

// ts-0025: an export/import round trip reproduces the bank under a target id.
func TestArchiveRoundTrip(t *testing.T) {
	source := seedStore(t)
	path := filepath.Join(t.TempDir(), "bank.json")

	if err := RunArchive(context.Background(), source, ArchiveFlags{Action: "export", Archive: path, TargetID: "old"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	target := storage.NewMemoryStore("public")
	if err := RunArchive(context.Background(), target, ArchiveFlags{Action: "import", Archive: path, TargetID: "imported"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	bank, err := target.GetBank(nil, "imported")
	if err != nil {
		t.Fatalf("the imported bank must exist: %v", err)
	}
	if bank.Name != "Old" {
		t.Fatalf("import must reproduce the bank profile: %+v", bank)
	}
	aliases, err := target.ListAliases(nil, "imported")
	if err != nil || len(aliases) != 1 || aliases[0].Alias != "nick" {
		t.Fatalf("import must reproduce the aliases: %+v, err = %v", aliases, err)
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

// ts-0025: the archive flags parse and require an archive and, for import, a
// target identifier.
func TestParseArchiveFlags(t *testing.T) {
	flags, err := ParseArchive("export", []string{"--archive", "f.json", "--target", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if flags.Archive != "f.json" || flags.Action != "export" {
		t.Fatalf("parsed flags = %+v", flags)
	}
	if _, err := ParseArchive("import", []string{"--archive", "f.json"}); err == nil {
		t.Fatal("an import without a target must be rejected")
	}
}
