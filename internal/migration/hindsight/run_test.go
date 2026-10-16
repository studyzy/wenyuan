package hindsight

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/storage"
)

// fixtureArchive builds a reference export archive with two documents.
func fixtureArchive(t *testing.T) *hindsightarchive.Archive {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bank.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
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
	write("manifest.json", `{"schema_version":1,"source_bank_id":"migrated-bank","archive_type":"bank"}`)
	write("documents/000000.json", `{"id":"doc-1","original_text":"The deployment runs on Tuesdays.",
	  "chunks":[{"chunk_index":0,"chunk_text":"The deployment runs on Tuesdays."}],
	  "facts":[
	    {"text":"The deployment runs on Tuesdays.","fact_type":"world","context":"ops schedule",
	     "event_date":"2026-01-02T00:00:00Z","occurred_start":"2026-01-02T00:00:00Z",
	     "entities":["Tuesday"],"tags":["ops"],"metadata":{"confidence_score":0.5}}
	  ]}`)
	write("documents/000001.json", `{"id":"doc-2","original_text":"Alice leads the team.",
	  "chunks":[{"chunk_index":0,"chunk_text":"Alice leads the team."}],
	  "facts":[
	    {"text":"Alice leads the team.","fact_type":"world","entities":["Alice"]},
	    {"text":"Alice joined in 2024.","fact_type":"experience","entities":["Alice"],
	     "causal_relations":[{"relation_type":"causes","target_fact_index":0}]}
	  ]}`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	archive, err := hindsightarchive.OpenZip(path)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

// The migration writes every document, chunk and fact from the archive into the
// target bank, and the facts remain recallable afterwards.
func TestRunMigratesArchiveIntoStore(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	archive := fixtureArchive(t)

	report, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Replace})
	if err != nil {
		t.Fatal(err)
	}
	if report.BankID != "migrated-bank" {
		t.Fatalf("bank id = %q", report.BankID)
	}
	if report.Documents != 2 || report.Chunks != 2 || report.Facts != 3 {
		t.Fatalf("counts wrong: %+v", report)
	}

	// The bank exists and is readable.
	if _, err := store.GetBank(ctx, "migrated-bank"); err != nil {
		t.Fatalf("migrated bank must exist: %v", err)
	}

	// Every migrated fact is recallable.
	result, err := store.Recall(ctx, storage.RecallQuery{BankID: "migrated-bank", Query: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) == 0 {
		t.Fatal("a migrated fact must be recallable")
	}
	found := false
	for _, match := range result.Matches {
		if match.Text == "The deployment runs on Tuesdays." {
			found = true
		}
	}
	if !found {
		t.Fatalf("the migrated fact was not recalled: %+v", result.Matches)
	}
}

// A migrated fact keeps the source context and event date now that the target
// has columns for them.
func TestRunPreservesSourceContextAndEventDate(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	archive := fixtureArchive(t)

	if _, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Replace}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Recall(ctx, storage.RecallQuery{BankID: "migrated-bank", Query: "deployment"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) == 0 {
		t.Fatal("expected a match")
	}
	match := result.Matches[0]
	if match.Context != "ops schedule" {
		t.Fatalf("context not preserved: %q", match.Context)
	}
	if match.OccurredStart == nil {
		t.Fatal("occurred_start not preserved")
	}
}

// A second run in Replace mode leaves the bank in the same state, so a retry is
// idempotent rather than duplicating rows.
func TestRunReplaceIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	archive := fixtureArchive(t)

	first, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Replace})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Replace})
	if err != nil {
		t.Fatal(err)
	}
	if first.Facts != second.Facts {
		t.Fatalf("replace run changed the fact count: %d then %d", first.Facts, second.Facts)
	}
	result, err := store.Recall(ctx, storage.RecallQuery{BankID: "migrated-bank", Query: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("expected 2 Alice facts after a replace re-run, got %d", len(result.Matches))
	}
}

// Append mode keeps an existing bank's rows and adds the archive's on top.
func TestRunAppendKeepsExistingRows(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	archive := fixtureArchive(t)

	if _, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Replace}); err != nil {
		t.Fatal(err)
	}
	// Write one extra unit into the same bank directly.
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{
		ID:       "pre-existing",
		BankID:   "migrated-bank",
		Text:     "A pre-existing memory about Alice.",
		FactType: storage.TypeWorld,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Append}); err != nil {
		t.Fatal(err)
	}
	result, err := store.Recall(ctx, storage.RecallQuery{BankID: "migrated-bank", Query: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	// 2 archive facts + 3 appended archive facts + 1 pre-existing = matches the
	// appended run not having deleted anything.
	seen := map[string]bool{}
	for _, match := range result.Matches {
		seen[match.ID] = true
	}
	if !seen["pre-existing"] {
		t.Fatal("append mode deleted a pre-existing row")
	}
}

// A dry run reports counts but writes nothing.
func TestRunDryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	archive := fixtureArchive(t)

	report, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Replace, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Facts != 3 {
		t.Fatalf("dry run counts = %+v", report)
	}
	if _, err := store.GetBank(ctx, "migrated-bank"); err == nil {
		t.Fatal("a dry run must not create the bank")
	}
}

// An explicit target bank id overrides the archive's source id.
func TestRunHonorsExplicitTargetBank(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	archive := fixtureArchive(t)

	report, err := Run(ctx, store, Mapping{Archive: archive, BankID: "target-bank"}, Options{Mode: Replace})
	if err != nil {
		t.Fatal(err)
	}
	if report.BankID != "target-bank" {
		t.Fatalf("bank id = %q", report.BankID)
	}
	if _, err := store.GetBank(ctx, "target-bank"); err != nil {
		t.Fatalf("explicit target bank must exist: %v", err)
	}
}

// Incremental mode writes only the documents the target does not already hold,
// so a re-run after further source writes is neither duplicating nor
// destructive.
func TestRunIncrementalWritesOnlyNewDocuments(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	archive := fixtureArchive(t)

	first, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Incremental})
	if err != nil {
		t.Fatal(err)
	}
	if first.Written != len(archive.Documents) || first.Skipped != 0 {
		t.Fatalf("first run = written %d skipped %d, want all written", first.Written, first.Skipped)
	}

	// Re-running the same archive must skip every document it already wrote.
	second, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Incremental})
	if err != nil {
		t.Fatal(err)
	}
	if second.Written != 0 || second.Skipped != len(archive.Documents) {
		t.Fatalf("re-run = written %d skipped %d, want none written", second.Written, second.Skipped)
	}

	// A document the target does not hold yet is written by the next run.
	archive.Documents = append(archive.Documents, hindsightarchive.Document{
		ID:           "doc-new",
		OriginalText: "A later write about Carol.",
		Facts:        []hindsightarchive.Fact{{Text: "Carol joined the team.", FactType: "world"}},
	})
	third, err := Run(ctx, store, Mapping{Archive: archive}, Options{Mode: Incremental})
	if err != nil {
		t.Fatal(err)
	}
	if third.Written != 1 || third.Skipped != len(archive.Documents)-1 {
		t.Fatalf("incremental run = written %d skipped %d, want only the new document", third.Written, third.Skipped)
	}
}
