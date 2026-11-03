package bankadmin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// writeExportFixture lays out an export archive the way `export-bank` does:
// one manifest, one banks.json, one file per document, and the bank's own
// observation, mental-model and knowledge-page sections.
func writeExportFixture(t *testing.T, dir string) {
	t.Helper()
	write := func(name, body string) {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", `{"schema_version":1,"source_bank_id":"coding-agent::wenyuan","document_count":1,"fact_count":2}`)
	write("banks.json", `[{"bank_id":"coding-agent::wenyuan","name":"coding-agent::wenyuan","disposition":{"empathy":3},"mission":"","config":{"enable_reranking":false}}]`)
	write("documents/000000.json", `{
	  "id": "conversation:one",
	  "original_text": "first turn",
	  "tags": ["source:chat"],
	  "created_at": "2026-10-12T17:13:03.109911Z",
	  "chunks": [{"chunk_index": 0, "chunk_text": "first turn"}],
	  "facts": [
	    {"source_id": "fact-a", "text": "fact one", "fact_type": "world", "chunk_index": 0,
	     "entities": ["wenyuan", "retain"], "tags": ["source:chat"], "metadata": {"source": "chat"},
	     "created_at": "2026-10-12T17:13:03Z", "mentioned_at": "2026-10-12T17:13:03Z"},
	    {"source_id": "fact-b", "text": "fact two", "fact_type": "experience", "chunk_index": 0,
	     "entities": ["retain"], "tags": ["source:chat"], "metadata": {}}
	  ]
	}`)
	write("observations.json", `[{"source_id":"obs-1","text":"an observation","tags":["consolidated"],
	  "proof_count":2,"created_at":"2026-10-13T00:00:00Z","consolidated_at":"2026-10-13T00:00:00Z",
	  "sources":[{"document_id":"conversation:one","fact_index":0},{"document_id":"conversation:one","fact_index":1}]},
	  {"source_id":"obs-orphan","text":"no evidence","sources":[{"document_id":"missing","fact_index":0}]}]`)
	write("mental_models.json", `[{"id":"mm-1","name":"Initiatives","content":"the page body","observations":{}}]`)
	write("knowledge_pages.json", `[{"id":"kf-1","parent_id":null,"kind":"folder","name":"Initiatives"},
	  {"id":"kp-1","parent_id":"kf-1","kind":"page","name":"Initiatives and enhancements","mental_model_id":"mm-1"}]`)
}

// A replay moves the bank's synthesized state, not just its documents: the
// facts keep their document anchor, an observation keeps the evidence it was
// folded from, and a knowledge page keeps the model whose content it reads.
func TestExportImportKeepsObservationsAndKnowledgePages(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	archive, err := ReadExportArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Manifest.SourceBankID != "coding-agent::wenyuan" || len(archive.Documents) != 1 {
		t.Fatalf("read archive = %+v", archive)
	}

	store := storage.NewMemoryStore("studyzy")
	ctx := context.Background()
	report, err := RunExportImport(ctx, store, archive, ExportImportOptions{BankID: "studyzy::wenyuan", Mode: ImportExportReplace})
	if err != nil {
		t.Fatal(err)
	}
	if report.DocumentsImported != 1 || report.FactsImported != 2 || report.ObservationsImported != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.ObservationsLost != 1 {
		t.Fatalf("observation with no backing fact counted %d lost, want 1", report.ObservationsLost)
	}
	if report.KnowledgePagesImported != 2 || report.MentalModelsImported != 1 {
		t.Fatalf("knowledge state = %+v", report)
	}
	if len(report.UnwrittenMentalModels) != 0 {
		t.Fatalf("models the store refused = %v", report.UnwrittenMentalModels)
	}

	bank, err := store.GetBank(ctx, "studyzy::wenyuan")
	if err != nil {
		t.Fatal(err)
	}
	if bank.Name != "studyzy::wenyuan" {
		t.Fatalf("bank is named %q, want the target id", bank.Name)
	}
	document, err := store.GetDocument(ctx, "studyzy::wenyuan", "conversation:one")
	if err != nil {
		t.Fatal(err)
	}
	if document.OriginalText != "first turn" || len(document.Tags) != 1 {
		t.Fatalf("document = %+v", document)
	}

	// The observation's evidence points at this bank's own facts, in document
	// order, and its proof count is the number of facts behind it.
	observationID := exportObservationUnitID("studyzy::wenyuan", "obs-1")
	observation, err := store.GetMemory(ctx, "studyzy::wenyuan", observationID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.FactType != storage.TypeObservation || observation.ProofCount != 2 {
		t.Fatalf("observation = %+v", observation)
	}
	wantEvidence := []string{
		exportFactID("studyzy::wenyuan", "conversation:one", 0),
		exportFactID("studyzy::wenyuan", "conversation:one", 1),
	}
	if len(observation.SourceMemoryIDs) != len(wantEvidence) {
		t.Fatalf("observation evidence = %v, want %v", observation.SourceMemoryIDs, wantEvidence)
	}
	for index, id := range wantEvidence {
		if observation.SourceMemoryIDs[index] != id {
			t.Fatalf("observation evidence = %v, want %v", observation.SourceMemoryIDs, wantEvidence)
		}
	}

	// The page reads through the model the archive carried, and its parent is
	// the folder that was written first.
	page, err := store.GetKnowledgeNode(ctx, "studyzy::wenyuan", "kp-1")
	if err != nil {
		t.Fatal(err)
	}
	if page.ParentID != "kf-1" || page.MentalModelID != "mm-1" {
		t.Fatalf("page = %+v", page)
	}
	model, err := store.GetMentalModel(ctx, "studyzy::wenyuan", "mm-1")
	if err != nil {
		t.Fatal(err)
	}
	if model.Content != "the page body" {
		t.Fatalf("model content = %q", model.Content)
	}

}

// The three modes differ in what an occupied target means, and each one is
// pinned: rename leaves the bank alone, replace rewrites it, merge keeps what
// is already there.
func TestExportImportModesOnAnOccupiedTarget(t *testing.T) {
	dir := t.TempDir()
	writeExportFixture(t, dir)
	archive, err := ReadExportArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewMemoryStore("studyzy")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "studyzy::wenyuan", Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDocument(ctx, "studyzy::wenyuan", "local-only", storage.Document{ID: "local-only", OriginalText: "kept"}); err != nil {
		t.Fatal(err)
	}

	// A mode-less replay is refused: destroying or renaming a live bank must be
	// the caller's stated choice.
	if _, err := RunExportImport(ctx, store, archive, ExportImportOptions{BankID: "studyzy::wenyuan"}); err == nil {
		t.Fatal("a replay without a mode was accepted")
	}

	renamed, err := RunExportImport(ctx, store, archive, ExportImportOptions{BankID: "studyzy::wenyuan", Mode: ImportExportRename})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.BankID != "studyzy::wenyuan-import" {
		t.Fatalf("rename target = %q", renamed.BankID)
	}
	if _, err := store.GetDocument(ctx, "studyzy::wenyuan", "local-only"); err != nil {
		t.Fatalf("rename touched the occupied bank: %v", err)
	}
	if _, err := store.GetDocument(ctx, renamed.BankID, "conversation:one"); err != nil {
		t.Fatalf("rename did not write the archive: %v", err)
	}

	// A merge keeps the target's own document and adds the archive's.
	if _, err := RunExportImport(ctx, store, archive, ExportImportOptions{BankID: "studyzy::wenyuan", Mode: ImportExportMerge}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetDocument(ctx, "studyzy::wenyuan", "local-only"); err != nil {
		t.Fatalf("merge dropped the target's own document: %v", err)
	}
	if _, err := store.GetDocument(ctx, "studyzy::wenyuan", "conversation:one"); err != nil {
		t.Fatalf("merge did not write the archive: %v", err)
	}

	// A replace clears the target first, so only the archive is left.
	if _, err := RunExportImport(ctx, store, archive, ExportImportOptions{BankID: "studyzy::wenyuan", Mode: ImportExportReplace}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetDocument(ctx, "studyzy::wenyuan", "local-only"); err == nil {
		t.Fatal("replace kept the target's own document")
	}
	if _, err := store.GetDocument(ctx, "studyzy::wenyuan", "conversation:one"); err != nil {
		t.Fatalf("replace did not write the archive: %v", err)
	}
}

// The reader sorts document sections by their number, so an archive with more
// than nine documents keeps the export's own order rather than a lexical one.
func TestReadExportArchiveOrdersDocumentSectionsNumerically(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000010.json", "000002.json", "000001.json"} {
		body, _ := json.Marshal(map[string]any{"id": name, "facts": []any{}})
		if err := os.WriteFile(filepath.Join(dir, "documents", name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "banks.json"), []byte(`[{"bank_id":"b"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := ReadExportArchive(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"000001.json", "000002.json", "000010.json"}
	for index, document := range archive.Documents {
		if document.ID != want[index] {
			t.Fatalf("document order = %v, want %v", idsOf(archive.Documents), want)
		}
	}
}

func idsOf(documents []ExportDocument) []string {
	out := make([]string, 0, len(documents))
	for _, document := range documents {
		out = append(out, document.ID)
	}
	return out
}
