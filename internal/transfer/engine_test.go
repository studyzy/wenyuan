package transfer

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/storage"
)

// fixedEmbeddings is a deterministic stand-in for a configured embedding
// model, so an import's re-embedding is observable without a provider.
type fixedEmbeddings struct{ calls int }

func (f *fixedEmbeddings) EmbedPassages(_ context.Context, texts []string) ([][]float32, error) {
	f.calls++
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = []float32{float32(len(texts[i])), 1, 0}
	}
	return vectors, nil
}

func (f *fixedEmbeddings) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 1, 0}, nil
}

// seedSourceBank builds a bank with one document, two facts and an observation
// over them, which is the shape the transfer stories exercise.
func seedSourceBank(t *testing.T, store *storage.MemoryStore) (string, map[string]string) {
	t.Helper()
	ctx := context.Background()
	const bank = "src"
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDocument(ctx, bank, "d1", storage.Document{
		ID: "d1", OriginalText: "Alice moved to Berlin. Alice renewed her lease.",
	}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for index, text := range []string{"Alice moved to Berlin | Involving: Alice", "Alice renewed her Berlin lease | Involving: Alice"} {
		unit := storage.MemoryUnit{
			ID:         archiveUnitID(bank, "d1", index),
			BankID:     bank,
			Text:       text,
			FactType:   storage.TypeWorld,
			DocumentID: "d1",
			Metadata:   map[string]any{},
		}
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResolveEntity(ctx, bank, "Alice", storage.EntityKindRegular, true); err != nil {
			t.Fatal(err)
		}
		ids[text] = unit.ID
	}
	observation := storage.MemoryUnit{
		ID:              archiveObservationID(bank, 0, "src-observation"),
		BankID:          bank,
		Text:            "Alice is settled in Berlin",
		FactType:        storage.TypeObservation,
		SourceMemoryIDs: []string{ids["Alice moved to Berlin | Involving: Alice"], ids["Alice renewed her Berlin lease | Involving: Alice"]},
		Metadata:        map[string]any{},
	}
	if _, err := store.UpsertUnit(ctx, observation); err != nil {
		t.Fatal(err)
	}
	return bank, ids
}

// An export carries the facts and, when asked, the observation with its
// evidence expressed as positions rather than source-instance ids.
func TestBuildArchiveCarriesFactsAndObservationEvidence(t *testing.T) {
	store := storage.NewMemoryStore("public")
	bank, ids := seedSourceBank(t, store)

	archive, err := BuildArchive(context.Background(), store, bank, ExportOptions{
		IncludeObservations: true,
		Scope:               DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Documents) != 1 || len(archive.Documents[0].Facts) != 2 {
		t.Fatalf("documents = %+v", archive.Documents)
	}
	seen := map[string]bool{}
	for _, fact := range archive.Documents[0].Facts {
		seen[fact.SourceID] = true
	}
	if !seen[ids["Alice moved to Berlin | Involving: Alice"]] || !seen[ids["Alice renewed her Berlin lease | Involving: Alice"]] {
		t.Fatalf("a fact must carry its source id, got %+v", seen)
	}
	if len(archive.Observations) != 1 {
		t.Fatalf("observations = %+v", archive.Observations)
	}
	sources := archive.Observations[0].Sources
	if len(sources) != 2 || sources[0].DocumentID != "d1" {
		t.Fatalf("observation sources = %+v", sources)
	}
	// A plain export leaves the derived layer behind.
	plain, err := BuildArchive(context.Background(), store, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Observations) != 0 {
		t.Fatalf("a plain export must not carry observations: %+v", plain.Observations)
	}
}

// The archive round-trips through its own writer and reader.
func TestArchiveWriteReadRoundTrip(t *testing.T) {
	store := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, store)
	archive, err := BuildArchive(context.Background(), store, bank, ExportOptions{
		IncludeObservations: true, IncludeKnowledgeBase: true, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := hindsightarchive.Write(archive)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := ParseArchive(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(reparsed.Documents) != 1 || reparsed.Documents[0].Facts[1].Text == "" {
		t.Fatalf("round trip lost the facts: %+v", reparsed.Documents)
	}
	if len(reparsed.Observations) != 1 || len(reparsed.Observations[0].Sources) != 2 {
		t.Fatalf("round trip lost the observation: %+v", reparsed.Observations)
	}
}

// A restore writes the memories into a fresh bank, re-embeds them, and rewrites
// an imported observation's evidence onto the facts that bank actually holds.
func TestRestoreRebuildsFactsAndRewritesObservationEvidence(t *testing.T) {
	store := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, store)
	archive, err := BuildArchive(context.Background(), store, bank, ExportOptions{
		IncludeObservations: true, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	embedder := &fixedEmbeddings{}
	report, err := ImportApplier{Store: store, Embeddings: embedder}.Apply(context.Background(), ImportRequest{
		Archive:      archive,
		BankID:       bank,
		TargetBankID: "dst",
		Mode:         ImportRestore,
		Scope:        DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.TargetBankID != "dst" || report.FactsImported != 2 || report.ObservationsImported != 1 {
		t.Fatalf("report = %+v", report)
	}
	if embedder.calls == 0 {
		t.Fatal("a restored fact must be re-embedded with the target's model")
	}
	page, err := store.ListMemories(context.Background(), storage.MemoryQuery{BankID: "dst", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]storage.MemoryUnit{}
	for _, unit := range page.Memories {
		byID[unit.ID] = unit
	}
	var observation storage.MemoryUnit
	for _, unit := range page.Memories {
		if unit.FactType == storage.TypeObservation {
			observation = unit
		}
	}
	if observation.ID == "" {
		t.Fatal("the observation did not arrive")
	}
	if len(observation.SourceMemoryIDs) != 2 {
		t.Fatalf("the imported observation lost its evidence: %+v", observation.SourceMemoryIDs)
	}
	for _, sourceID := range observation.SourceMemoryIDs {
		source, ok := byID[sourceID]
		if !ok {
			t.Fatalf("evidence %s does not exist in the target bank", sourceID)
		}
		if source.FactType == storage.TypeObservation {
			t.Fatalf("evidence %s is itself an observation", sourceID)
		}
	}
	// The source bank is untouched: a migration is a copy until someone says
	// otherwise.
	source, err := store.ListMemories(context.Background(), storage.MemoryQuery{BankID: bank, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if source.Total != 3 {
		t.Fatalf("the source bank changed: %d memories", source.Total)
	}
}

// A restore into a bank that already exists is refused rather than merged.
func TestRestoreRefusesAnExistingTarget(t *testing.T) {
	store := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, store)
	archive, err := BuildArchive(context.Background(), store, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ImportApplier{Store: store}.Apply(context.Background(), ImportRequest{
		Archive: archive, BankID: bank, TargetBankID: bank, Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err == nil || !contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want an already-exists refusal", err)
	}
}

// A merge into a bank that does not exist yet creates it, so a first import
// into a fresh identifier works.
func TestMergeCreatesItsTarget(t *testing.T) {
	store := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, store)
	archive, err := BuildArchive(context.Background(), store, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatal(err)
	}
	report, err := ImportApplier{Store: store}.Apply(context.Background(), ImportRequest{
		Archive: archive, BankID: "fresh", Mode: ImportMerge, OnConflict: ConflictSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.FactsImported != 2 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := store.GetBank(context.Background(), "fresh"); err != nil {
		t.Fatalf("the merge target must exist: %v", err)
	}
}

// A skip re-import leaves the target's documents alone, so nothing is
// replayed: an observation whose evidence did not come back is counted as
// skipped rather than silently re-pointed at the facts the target already
// holds, and the merge reports the documents it skipped by id.
func TestMergeSkipCountsSkippedObservations(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	archive, err := BuildArchive(ctx, source, bank, ExportOptions{
		IncludeObservations: true, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	target := storage.NewMemoryStore("public")
	applier := ImportApplier{Store: target}
	if _, err := applier.Apply(ctx, ImportRequest{Archive: archive, BankID: "dst", Mode: ImportMerge}); err != nil {
		t.Fatal(err)
	}
	report, err := applier.Apply(ctx, ImportRequest{
		Archive: archive, BankID: "dst", Mode: ImportMerge, OnConflict: ConflictSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.DocumentsSkipped != 1 || report.DocumentsImported != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.SkippedDocumentIDs) != 1 || report.SkippedDocumentIDs[0] != "d1" {
		t.Fatalf("skipped ids = %v", report.SkippedDocumentIDs)
	}
	if report.ObservationsSkipped != 1 || report.ObservationsImported != 0 {
		t.Fatalf("a skipped document leaves its observations skipped: %+v", report)
	}
}

// A merge and a whole-bank restore report different result_metadata
// vocabularies: the merge carries the document/observation counts and the
// skipped/remapped id maps, the restore carries the per-component counts and
// the bank it created.
func TestImportMetadataVocabularyIsModeSpecific(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	archive, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatal(err)
	}
	mergeReport, err := ImportApplier{Store: storage.NewMemoryStore("public")}.Apply(ctx, ImportRequest{
		Archive: archive, BankID: "merged", Mode: ImportMerge,
	})
	if err != nil {
		t.Fatal(err)
	}
	restoreReport, err := ImportApplier{Store: storage.NewMemoryStore("public")}.Apply(ctx, ImportRequest{
		Archive: archive, TargetBankID: "restored", Mode: ImportRestore,
	})
	if err != nil {
		t.Fatal(err)
	}
	mergeKeys := sortedKeys(mergeReport.Metadata())
	wantMerge := []string{
		"documents_imported", "documents_skipped", "facts_imported",
		"knowledge_pages_imported", "mental_models_imported",
		"observations_imported", "observations_skipped",
		"remapped_document_ids", "skipped_document_ids",
	}
	if strings.Join(mergeKeys, ",") != strings.Join(wantMerge, ",") {
		t.Fatalf("merge metadata keys = %v, want %v", mergeKeys, wantMerge)
	}
	restoreMetadata := restoreReport.Metadata()
	if restoreMetadata["restored_bank_id"] != "restored" {
		t.Fatalf("restore metadata must name the bank it created: %v", restoreMetadata)
	}
	if _, ok := restoreMetadata["documents_skipped"]; ok {
		t.Fatalf("restore metadata carries no documents_skipped: %v", restoreMetadata)
	}
	wantRestore := []string{
		"attachments_imported", "directives_imported", "documents_imported",
		"facts_imported", "history_rows_imported", "invalidated_memories_imported",
		"knowledge_pages_imported", "maintenance_queue_rows_imported",
		"mental_model_history_imported", "mental_models_imported",
		"observations_imported", "operations_imported", "restored_bank_id",
		"target_bank_id", "webhooks_imported",
	}
	if got := sortedKeys(restoreMetadata); strings.Join(got, ",") != strings.Join(wantRestore, ",") {
		t.Fatalf("restore metadata keys = %v, want %v", got, wantRestore)
	}
}

// sortedKeys returns a metadata map's keys in sorted order, so a test can pin
// the exact key set the reference emits without depending on map iteration.
func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// A restore re-keys the directives it copies rather than carrying the source's
// id. A directive's id is unique across the whole schema, so a copy that kept
// it would resolve onto the still-present original on a same-instance transfer:
// the write reports the row imported while the copy owns none of it. The
// package-level pin is the id change; the system story exercises the collision
// itself, which only a backend with a schema-wide key reproduces.
func TestRestoreReKeysCopiedDirectives(t *testing.T) {
	store := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, store)
	ctx := context.Background()
	const sourceDirectiveID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	if _, err := store.PutDirective(ctx, storage.Directive{
		ID: sourceDirectiveID, BankID: bank, Name: "house-style",
		Content: "Answer in two sentences.", Priority: 1, IsActive: true,
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := BuildArchive(ctx, store, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Directives) != 1 {
		t.Fatalf("the export lost the directive: %+v", archive.Directives)
	}
	report, err := ImportApplier{Store: store}.Apply(ctx, ImportRequest{
		Archive: archive, BankID: bank, TargetBankID: "dst", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.DirectivesImported != 1 {
		t.Fatalf("report = %+v", report)
	}
	copied, err := store.ListDirectives(ctx, "dst")
	if err != nil {
		t.Fatal(err)
	}
	if len(copied) != 1 || copied[0].Name != "house-style" {
		t.Fatalf("the copy's directives = %+v", copied)
	}
	if copied[0].ID == sourceDirectiveID {
		t.Fatalf("the copy kept the source's directive id %s, which collides on a same-instance transfer", copied[0].ID)
	}
	source, err := store.ListDirectives(ctx, bank)
	if err != nil {
		t.Fatal(err)
	}
	if len(source) != 1 || source[0].ID != sourceDirectiveID {
		t.Fatalf("the source's directive changed: %+v", source)
	}
}

// contains reports whether a string contains another, kept local so the test
// does not import a matcher for one assertion.
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// seedExportAttachment gives one document an attachment whose bytes live in
// stored file storage, which is the shape the archive has to carry.
func seedExportAttachment(t *testing.T, store *storage.MemoryStore, bank, documentID string) storage.Attachment {
	t.Helper()
	ctx := context.Background()
	key := storage.AttachmentStorageKey(bank, documentID, "hash-1")
	if err := store.PutStoredFile(ctx, storage.StoredFile{
		Key: key, ContentType: "image/png", Data: []byte("png bytes"),
	}); err != nil {
		t.Fatal(err)
	}
	attachment := storage.Attachment{
		BankID: bank, DocumentID: documentID, AttachmentHash: "hash-1", ShortID: "abc123",
		MediaType: "image/png", ByteSize: 9, StorageKey: key, Kind: "image", Filename: "photo.png",
	}
	if err := store.PutAttachment(ctx, attachment); err != nil {
		t.Fatal(err)
	}
	return attachment
}

// An export carries every attachment's row and bytes, and an import writes them
// back under a key recomputed for the target bank.
func TestAttachmentsTravelWithTheirBytes(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	seedExportAttachment(t, source, bank, "d1")

	archive, err := BuildArchive(ctx, source, bank, ExportOptions{
		Scope:                DefaultExportScope(),
		IncludeObservations:  true,
		IncludeKnowledgeBase: true,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(archive.Attachments) != 1 || archive.Manifest.AttachmentCount != 1 {
		t.Fatalf("attachments = %+v (count %d)", archive.Attachments, archive.Manifest.AttachmentCount)
	}
	row := archive.Attachments[0]
	if row.DocumentID != "d1" || row.Filename != "photo.png" || row.ShortID != "abc123" {
		t.Fatalf("row = %+v", row)
	}
	if string(archive.Blobs[row.Entry]) != "png bytes" {
		t.Fatalf("blob = %q", archive.Blobs[row.Entry])
	}

	target := storage.NewMemoryStore("public")
	report, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, TargetBankID: "copy", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.AttachmentsImported != 1 || report.AttachmentsSkipped != 0 {
		t.Fatalf("report = %+v", report)
	}
	restored, err := target.GetAttachment(ctx, "copy", "abc123")
	if err != nil {
		t.Fatalf("attachment must exist: %v", err)
	}
	if restored.Filename != "photo.png" || restored.MediaType != "image/png" || restored.ByteSize != 9 {
		t.Fatalf("restored row = %+v", restored)
	}
	// The key is the target's own, not the source's.
	if want := storage.AttachmentStorageKey("copy", restored.DocumentID, "hash-1"); restored.StorageKey != want {
		t.Fatalf("storage key = %q, want %q", restored.StorageKey, want)
	}
	file, err := target.GetStoredFile(ctx, restored.StorageKey)
	if err != nil || string(file.Data) != "png bytes" {
		t.Fatalf("bytes = %q, err = %v", file.Data, err)
	}
}

// An attachment whose bytes storage does not hold is skipped rather than
// carried as an empty blob.
func TestExportSkipsAnAttachmentWithoutBytes(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, store)
	if err := store.PutAttachment(ctx, storage.Attachment{
		BankID: bank, DocumentID: "d1", AttachmentHash: "gone", ShortID: "zzz",
		MediaType: "image/png", StorageKey: storage.AttachmentStorageKey(bank, "d1", "gone"), Kind: "image",
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := BuildArchive(ctx, store, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(archive.Attachments) != 0 || archive.Manifest.AttachmentCount != 0 {
		t.Fatalf("a row without bytes must not travel: %+v", archive.Attachments)
	}
}

// A merge is the reference's document import: it folds in the archive's
// documents, facts and observations and nothing else, so an attachment the
// archive carries is deliberately not restored — only a whole-bank restore
// writes attachments back.
func TestMergeDoesNotRestoreAttachments(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	seedExportAttachment(t, source, bank, "d1")

	archive, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(archive.Attachments) != 1 {
		t.Fatalf("archive should carry the attachment: %+v", archive.Attachments)
	}

	target := storage.NewMemoryStore("public")
	report, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, BankID: "dst", Mode: ImportMerge,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.AttachmentsImported != 0 || report.AttachmentsSkipped != 0 {
		t.Fatalf("a merge must not touch attachments: %+v", report)
	}
	if _, err := target.GetAttachment(ctx, "dst", "abc123"); err == nil {
		t.Fatal("a merged bank must not hold the archive's attachment")
	}
}

// A model's refresh log rides with the model, and an import replays it onto the
// model it belongs to.
func TestMentalModelHistoryTravels(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	if _, err := source.PutMentalModel(ctx, storage.MentalModel{
		ID: "mm-1", BankID: bank, Subtype: storage.SubtypePinned,
		Name: "Notes", SourceQuery: "q", MaxTokens: 100, Content: "now",
	}); err != nil {
		t.Fatal(err)
	}
	previous := "before"
	for _, at := range []time.Time{
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		if err := source.RecordMentalModelHistory(ctx, bank, "mm-1", storage.MentalModelHistoryEntry{
			ChangedAt:       at,
			PreviousContent: &previous,
		}); err != nil {
			t.Fatal(err)
		}
	}

	archive, err := BuildArchive(ctx, source, bank, ExportOptions{
		Scope: DefaultExportScope(), IncludeKnowledgeBase: true,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(archive.MentalModelHistory) != 2 {
		t.Fatalf("history rows = %d, want 2", len(archive.MentalModelHistory))
	}
	// Oldest first, and without the surrogate row id the target reassigns.
	if archive.MentalModelHistory[0]["changed_at"] == archive.MentalModelHistory[1]["changed_at"] {
		t.Fatalf("rows are not ordered: %+v", archive.MentalModelHistory)
	}
	if _, hasID := archive.MentalModelHistory[0]["id"]; hasID {
		t.Fatalf("the surrogate row id must not travel: %+v", archive.MentalModelHistory[0])
	}

	target := storage.NewMemoryStore("public")
	report, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, TargetBankID: "copy", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.MentalModelHistory != 2 {
		t.Fatalf("report = %+v", report)
	}
	entries, err := target.MentalModelHistory(ctx, "copy", "mm-1")
	if err != nil {
		t.Fatalf("history read: %v", err)
	}
	if len(entries) != 2 || entries[0].PreviousContent == nil || *entries[0].PreviousContent != "before" {
		t.Fatalf("restored history = %+v", entries)
	}
}

// The operational trail travels only when the history section was selected, and
// is replayed when the restore asks for it.
func TestOperationalHistoryTravelsOnlyWhenSelected(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	endpoint := "http://example.test"
	if err := source.RecordAuditLog(ctx, storage.AuditEntry{
		ID: "a-1", Action: "recall", Transport: "http", BankID: bank,
		StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	tokens := 7
	if err := source.RecordLLMRequest(ctx, storage.LLMRequest{
		ID: "l-1", BankID: bank, Operation: "retain", Provider: "mock", Model: "m",
		Status: "ok", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		TotalTokens: &tokens, Metadata: map[string]any{"endpoint": endpoint},
	}); err != nil {
		t.Fatal(err)
	}

	withoutHistory, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(withoutHistory.History) != 0 {
		t.Fatalf("history must not travel by default: %+v", withoutHistory.History)
	}

	scope := DefaultExportScope()
	scope.History = true
	withHistory, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: scope})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(withHistory.History[hindsightarchive.AuditLogTable]) != 1 ||
		len(withHistory.History[hindsightarchive.LLMRequestsTable]) != 1 {
		t.Fatalf("history = %+v", withHistory.History)
	}
	if !withHistory.Manifest.IncludesHistory {
		t.Fatal("the manifest must record that history travelled")
	}

	target := storage.NewMemoryStore("public")
	report, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: withHistory, TargetBankID: "copy", Mode: ImportRestore, Scope: scope,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.HistoryRows != 2 {
		t.Fatalf("report = %+v", report)
	}
	page, err := target.ListAuditLog(ctx, storage.AuditQuery{BankID: "copy", Limit: 10})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Action != "recall" {
		t.Fatalf("audit entries = %+v, err = %v", page.Entries, err)
	}
	requests, err := target.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: "copy", Limit: 10})
	if err != nil || len(requests.Requests) != 1 || requests.Requests[0].TotalTokens == nil || *requests.Requests[0].TotalTokens != 7 {
		t.Fatalf("model requests = %+v, err = %v", requests.Requests, err)
	}
}

// The curation archive rides with the data section: a unit the caller
// invalidated is re-archived in the target bank, under the target's own id.
func TestInvalidatedMemoriesTravel(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	if _, err := source.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "gone-1", BankID: bank, Text: "Alice used to live in Bonn", FactType: storage.TypeWorld,
		Metadata: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	reason := "superseded"
	if _, err := source.UpdateMemory(ctx, bank, "gone-1", storage.MemoryPatch{
		State: strPtrState("invalidated"), Reason: &reason,
	}); err != nil {
		t.Fatal(err)
	}

	archive, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rows := archive.Data[hindsightarchive.InvalidatedMemoryTable]
	if len(rows) != 1 {
		t.Fatalf("invalidated rows = %+v", archive.Data)
	}
	if archive.Manifest.InvalidatedMemoryCount != 1 {
		t.Fatalf("manifest count = %d", archive.Manifest.InvalidatedMemoryCount)
	}
	if rows[0]["invalidation_reason"] != "superseded" {
		t.Fatalf("row = %+v", rows[0])
	}

	target := storage.NewMemoryStore("public")
	report, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, TargetBankID: "copy", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.InvalidatedMemories != 1 {
		t.Fatalf("report = %+v", report)
	}
	page, err := target.ListMemories(ctx, storage.MemoryQuery{BankID: "copy", State: "invalidated", Limit: 10})
	if err != nil || len(page.Memories) != 1 {
		t.Fatalf("archived memories = %+v, err = %v", page.Memories, err)
	}
	if page.Memories[0].Text != "Alice used to live in Bonn" || page.Memories[0].InvalidationReason == nil ||
		*page.Memories[0].InvalidationReason != "superseded" {
		t.Fatalf("restored unit = %+v", page.Memories[0])
	}
}

// strPtrState renders a curation-state patch value.
func strPtrState(value string) *string { return &value }

// The operations log travels with the data section verbatim -- every row,
// in-flight ones included -- because the archive is a dump of the table: the
// reference exports with `SELECT *` (engine/transfer/export.py:431-440). The
// in-flight rows are dropped on restore instead
// (engine/transfer/importer.py:775-793), since a restored `pending` row is work
// the target's worker would actually run.
func TestOperationsLogTravelsVerbatimAndRestoresFinishedOnly(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	// Seeded through RestoreOperation so the source row carries a real created_at;
	// PutOperation would stamp the current time and the assertion below could not
	// tell a preserved timestamp from a restamped one.
	archivedCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := source.RestoreOperation(ctx, storage.Operation{
		OperationID: "op-done", BankID: bank, OperationType: "retain",
		Status: storage.StatusCompleted, CreatedAt: archivedCreated, UpdatedAt: archivedCreated,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.PutOperation(ctx, storage.Operation{
		OperationID: "op-pending", BankID: bank, OperationType: "retain", Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}

	archive, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rows := archive.Data[hindsightarchive.AsyncOperationsTable]
	if len(rows) != 2 {
		t.Fatalf("the archive must carry every row, live ones included: %+v", rows)
	}
	if archive.Manifest.OperationCount != 2 {
		t.Fatalf("manifest operation count = %d", archive.Manifest.OperationCount)
	}
	// The archived row is the table row: the reference's dump is `SELECT *`, so
	// an importer written against it reads these names.
	for _, column := range []string{
		"operation_id", "bank_id", "operation_type", "status", "error_message",
		"created_at", "updated_at", "completed_at", "retry_count", "next_retry_at",
		"serialization_key", "task_payload", "result_metadata", "worker_id", "claimed_at",
	} {
		if _, carried := rows[0][column]; !carried {
			t.Fatalf("archived row is missing column %q: %+v", column, rows[0])
		}
	}
	for _, invented := range []string{"payload", "error", "is_parent"} {
		if _, carried := rows[0][invented]; carried {
			t.Fatalf("archived row must not carry invented member %q: %+v", invented, rows[0])
		}
	}

	target := storage.NewMemoryStore("public")
	report, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, TargetBankID: "copy", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.Operations != 1 {
		t.Fatalf("report = %+v", report)
	}
	page, err := target.ListOperations(ctx, storage.OperationQuery{BankID: "copy", Limit: 10})
	if err != nil || len(page.Operations) != 1 {
		t.Fatalf("operations = %+v, err = %v", page.Operations, err)
	}
	restored := page.Operations[0]
	if restored.Status != storage.StatusCompleted {
		t.Fatalf("restored operation = %+v", restored)
	}
	// The row keeps the id the archive carries: the reference inserts every
	// column of the dumped row (`_restore_rows`, `INSERT ... ON CONFLICT DO
	// NOTHING`), so a restore is idempotent rather than duplicating the log.
	if restored.OperationID != "op-done" {
		t.Fatalf("restored operation id = %q, want the archived id", restored.OperationID)
	}
	// The archived timestamps survive: the reference inserts the dumped row column
	// for column, so a restored operation reports when it ran, not when it was
	// restored. The import replays through RestoreOperation for this reason.
	if !restored.CreatedAt.Equal(archivedCreated) {
		t.Fatalf("restored created_at = %s, want the archived %s", restored.CreatedAt, archivedCreated)
	}
}

// An archived row already present under the same id is not counted: an archived
// unit keeps its memory id, so a same-instance copy finds the source's row
// already holding it, and the report must say what actually landed.
func TestInvalidatedMemoryCountsOnlyWhatLanded(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	if _, err := source.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "gone-2", BankID: bank, Text: "Alice used to live in Bonn", FactType: storage.TypeWorld,
		Metadata: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.UpdateMemory(ctx, bank, "gone-2", storage.MemoryPatch{State: strPtrState("invalidated")}); err != nil {
		t.Fatal(err)
	}
	archive, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// The same archive applied twice to one store: the second finds the row
	// already archived and reports nothing rather than claiming a duplicate.
	target := storage.NewMemoryStore("public")
	first, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, TargetBankID: "copy", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if first.InvalidatedMemories != 1 {
		t.Fatalf("first report = %+v", first)
	}
	second, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, TargetBankID: "copy-again", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if second.InvalidatedMemories != 0 {
		t.Fatalf("second report = %+v, want no new archived memories", second)
	}
}

// A fact's observation scoping is the reference's union type: a mode-name
// string or an explicit list of tag groups. Both shapes must survive an
// archive written by the reference, which is what an archive produced with
// scoped consolidation carries.
func TestFactObservationScopesRoundTripBothShapes(t *testing.T) {
	for name, stored := range map[string]string{
		"scope enum": `"shared"`,
		"tag groups": `[["alice","berlin"],["lease"]]`,
	} {
		t.Run(name, func(t *testing.T) {
			archive := &hindsightarchive.Archive{
				Manifest: hindsightarchive.Manifest{
					SchemaVersion: hindsightarchive.SupportedSchemaVersion,
					SourceBankID:  "src", ArchiveType: "bank",
					DocumentCount: 1, FactCount: 1,
				},
				Documents: []hindsightarchive.Document{{
					ID: "d1", OriginalText: "Alice moved to Berlin.",
					Chunks: []hindsightarchive.Chunk{{ChunkIndex: 0, ChunkText: "Alice moved to Berlin."}},
					Facts: []hindsightarchive.Fact{{
						Text: "Alice moved to Berlin", FactType: "world",
						ObservationScopes: json.RawMessage(stored),
					}},
				}},
			}
			// The reference's own reader must accept this archive: the field
			// was historically a []string, which refused the enum shape.
			data, err := hindsightarchive.Write(archive)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			reparsed, err := ParseArchive(data)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := string(reparsed.Documents[0].Facts[0].ObservationScopes); got != stored {
				t.Fatalf("parse re-encoded the scoping: got %s, want %s", got, stored)
			}

			store := storage.NewMemoryStore("public")
			embedder := &fixedEmbeddings{}
			if _, err := (ImportApplier{Store: store, Embeddings: embedder}).Apply(context.Background(), ImportRequest{
				Archive:      reparsed,
				BankID:       "src",
				TargetBankID: "dst",
				Mode:         ImportRestore,
				Scope:        DefaultExportScope(),
			}); err != nil {
				t.Fatalf("import: %v", err)
			}
			page, err := store.ListMemories(context.Background(), storage.MemoryQuery{BankID: "dst", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Memories) != 1 {
				t.Fatalf("memories = %d, want 1", len(page.Memories))
			}
			if got := string(page.Memories[0].ObservationScopes); got != stored {
				t.Fatalf("import rewrote the scoping: got %s, want %s", got, stored)
			}
		})
	}
}

// orderRecordingStore records whether each link is written only after every
// unit has landed, which is the order the link's foreign key requires. The
// in-memory store is permissive, so the ordering has to be asserted here
// rather than by a rejected write.
type orderRecordingStore struct {
	*storage.MemoryStore
	units      int
	links      int
	violations int
}

func (s *orderRecordingStore) UpsertUnit(ctx context.Context, unit storage.MemoryUnit) (storage.MemoryUnit, error) {
	s.units++
	return s.MemoryStore.UpsertUnit(ctx, unit)
}

func (s *orderRecordingStore) UpsertLink(ctx context.Context, link storage.MemoryLink) error {
	s.links++
	if s.units < 3 {
		s.violations++
	}
	return s.MemoryStore.UpsertLink(ctx, link)
}

// A causal edge may name a fact that follows its source in the document, which
// is what the upstream producer emits. Every fact must land before any edge is
// written, or the forward edge violates the link's foreign key.
func TestCausalEdgesMayPointForward(t *testing.T) {
	archive := &hindsightarchive.Archive{
		Manifest: hindsightarchive.Manifest{
			SchemaVersion: hindsightarchive.SupportedSchemaVersion,
			SourceBankID:  "src", ArchiveType: "bank",
			DocumentCount: 1, FactCount: 3,
		},
		Documents: []hindsightarchive.Document{{
			ID: "d1", OriginalText: "Alice moved. Then Alice renewed. So the lease held.",
			Chunks: []hindsightarchive.Chunk{{ChunkIndex: 0, ChunkText: "Alice moved. Then Alice renewed. So the lease held."}},
			Facts: []hindsightarchive.Fact{
				{Text: "Alice moved", FactType: "world",
					CausalRelations: []hindsightarchive.CausalRelation{{RelationType: "causes", TargetFactIndex: 2}}},
				{Text: "Alice renewed", FactType: "world"},
				{Text: "the lease held", FactType: "world",
					CausalRelations: []hindsightarchive.CausalRelation{{RelationType: "caused_by", TargetFactIndex: 1}}},
			},
		}},
	}
	store := &orderRecordingStore{MemoryStore: storage.NewMemoryStore("public")}
	if _, err := (ImportApplier{Store: store, Embeddings: &fixedEmbeddings{}}).Apply(context.Background(), ImportRequest{
		Archive: archive, BankID: "src", TargetBankID: "dst",
		Mode: ImportRestore, Scope: DefaultExportScope(),
	}); err != nil {
		t.Fatalf("import with a forward causal edge: %v", err)
	}
	if store.violations != 0 {
		t.Fatalf("link written before every unit landed %d times; links must follow all units", store.violations)
	}
	if store.links != 2 {
		t.Fatalf("links = %d, want 2", store.links)
	}
}

// test-import-rebuilds-entity-cooccurrences: entity co-occurrences are derived
// data the archive never carries, so a restore has to rebuild them from the
// facts it writes. Without that the entity graph of an imported bank has no
// edges at all — the relations view of an imported bank draws nothing.
func TestRestoreRebuildsEntityCooccurrences(t *testing.T) {
	ctx := context.Background()
	archive := &hindsightarchive.Archive{
		Manifest: hindsightarchive.Manifest{
			SchemaVersion: hindsightarchive.SupportedSchemaVersion,
			SourceBankID:  "src",
			ArchiveType:   "bank",
		},
		Documents: []hindsightarchive.Document{{
			ID:           "d1",
			OriginalText: "Alice moved to Berlin.",
			Facts: []hindsightarchive.Fact{{
				Text: "Alice moved to Berlin.", FactType: "world",
				// The same entity twice, so the pair is counted once for this
				// unit rather than once per mention.
				Entities: []string{"Alice", "Berlin", "Alice"},
			}},
		}},
	}
	store := storage.NewMemoryStore("public")
	if _, err := (ImportApplier{Store: store}).Apply(ctx, ImportRequest{
		Archive: archive, BankID: "src", TargetBankID: "dst",
		Mode: ImportRestore, Scope: DefaultExportScope(),
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	graph, err := store.EntityGraph(ctx, storage.EntityGraphQuery{BankID: "dst", Limit: 100, MinCount: 1})
	if err != nil {
		t.Fatalf("entity graph: %v", err)
	}
	if len(graph.Nodes) != 2 {
		t.Fatalf("graph nodes = %d, want the fact's two entities", len(graph.Nodes))
	}
	if len(graph.Edges) != 1 {
		t.Fatalf("graph edges = %d, want the one pair the fact witnesses", len(graph.Edges))
	}
	if graph.Edges[0].Weight != 1 {
		t.Fatalf("edge weight = %d, want 1: a repeated entity counts once", graph.Edges[0].Weight)
	}
}

// test-import-rebuilds-document-chunks: the archive carries each document's
// chunk text, and the target rebuilds the chunk rows from it — the reference
// classifies chunks as replayed data — so a fact that names a chunk is
// addressed by a row that exists in the imported bank. Without this an imported
// bank has no chunks at all and the fact's reference would have nowhere to
// point.
func TestRestoreRebuildsDocumentChunks(t *testing.T) {
	ctx := context.Background()
	archive := &hindsightarchive.Archive{
		Manifest: hindsightarchive.Manifest{
			SchemaVersion: hindsightarchive.SupportedSchemaVersion,
			SourceBankID:  "src",
			ArchiveType:   "bank",
		},
		Documents: []hindsightarchive.Document{{
			ID:           "d1",
			OriginalText: "The deployment runs on Tuesdays. Alice leads the team.",
			Chunks: []hindsightarchive.Chunk{
				{ChunkIndex: 0, ChunkText: "The deployment runs on Tuesdays."},
				{ChunkIndex: 1, ChunkText: "Alice leads the team."},
			},
			Facts: []hindsightarchive.Fact{{
				Text: "Alice leads the team.", FactType: "world",
				ChunkIndex: intPtr(1),
			}},
		}},
	}
	store := storage.NewMemoryStore("public")
	if _, err := (ImportApplier{Store: store}).Apply(ctx, ImportRequest{
		Archive: archive, BankID: "src", TargetBankID: "dst",
		Mode: ImportRestore, Scope: DefaultExportScope(),
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	page, err := store.ListChunks(ctx, storage.ChunkQuery{BankID: "dst", DocumentID: "d1", Limit: 10})
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("chunks stored = %d, want the document's two", page.Total)
	}
	for _, chunk := range page.Chunks {
		want := archive.Documents[0].Chunks[chunk.ChunkIndex].ChunkText
		if chunk.ChunkText != want {
			t.Fatalf("chunk %d text = %q, want %q", chunk.ChunkIndex, chunk.ChunkText, want)
		}
		if chunk.ContentHash != storage.ContentHash(want) {
			t.Fatalf("chunk %d hash = %q, want the hash of its text", chunk.ChunkIndex, chunk.ContentHash)
		}
	}

	// The fact points at the chunk row it came from, which is what the
	// addressed chunk route and a delta retain both read.
	memories, err := store.ListMemories(ctx, storage.MemoryQuery{BankID: "dst", Limit: 10})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	if len(memories.Memories) != 1 {
		t.Fatalf("memories = %d, want 1", len(memories.Memories))
	}
	wantID := storage.BuildChunkID("dst", "d1", 1)
	if got := memories.Memories[0].ChunkID; got != wantID {
		t.Fatalf("fact chunk id = %q, want %q", got, wantID)
	}
	if _, err := store.GetChunk(ctx, "dst", wantID); err != nil {
		t.Fatalf("the fact's chunk id must resolve in the imported bank: %v", err)
	}
}

// A deployment that keeps no source text still records the chunk rows and their
// content hashes, so a delta retain recognises a chunk whose text it does not
// hold.
func TestRestoreDiscardsChunkTextWhenConfigured(t *testing.T) {
	ctx := context.Background()
	const text = "The deployment runs on Tuesdays."
	archive := &hindsightarchive.Archive{
		Manifest: hindsightarchive.Manifest{
			SchemaVersion: hindsightarchive.SupportedSchemaVersion,
			SourceBankID:  "src",
			ArchiveType:   "bank",
		},
		Documents: []hindsightarchive.Document{{
			ID:     "d1",
			Chunks: []hindsightarchive.Chunk{{ChunkIndex: 0, ChunkText: text}},
			Facts:  []hindsightarchive.Fact{{Text: text, FactType: "world"}},
		}},
	}
	store := storage.NewMemoryStore("public")
	if _, err := (ImportApplier{Store: store, DiscardDocumentText: true}).Apply(ctx, ImportRequest{
		Archive: archive, BankID: "src", TargetBankID: "dst",
		Mode: ImportRestore, Scope: DefaultExportScope(),
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	chunk, err := store.GetChunk(ctx, "dst", storage.BuildChunkID("dst", "d1", 0))
	if err != nil {
		t.Fatalf("get chunk: %v", err)
	}
	if chunk.ChunkText != "" {
		t.Fatalf("chunk text = %q, want none when the deployment keeps no source text", chunk.ChunkText)
	}
	if chunk.ContentHash != storage.ContentHash(text) {
		t.Fatalf("chunk hash = %q, want the hash of the discarded text", chunk.ContentHash)
	}
}

// intPtr returns a pointer to the value, for the optional members of an
// archive fact.
func intPtr(value int) *int { return &value }

// test-archive-carries-operational-history: the operational-history section is
// the pair of tables the reference writes under history/ and restores only when
// the caller asked for it. This pins both halves on the in-process store, so the
// section cannot go missing (or become unconditional) unnoticed.
func TestArchiveCarriesOperationalHistoryOnRequest(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	const bank = "hist-src"
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: bank}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAuditLog(ctx, storage.AuditEntry{
		Action: "retain", Transport: "http", BankID: bank, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordLLMRequest(ctx, storage.LLMRequest{
		BankID: bank, Operation: "retain", Provider: "mock", Model: "m",
		Status: "success", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Without the section selected the history rows stay behind.
	plain, err := BuildArchive(ctx, store, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatalf("export without history: %v", err)
	}
	if len(plain.History) != 0 {
		t.Fatalf("an export that did not ask for history carried %v", plain.History)
	}

	withHistory, err := BuildArchive(ctx, store, bank, ExportOptions{
		Scope: ExportScope{Data: true, BankConfig: true, History: true},
	})
	if err != nil {
		t.Fatalf("export with history: %v", err)
	}
	if len(withHistory.History[hindsightarchive.AuditLogTable]) != 1 ||
		len(withHistory.History[hindsightarchive.LLMRequestsTable]) != 1 {
		t.Fatalf("history sections = %v, want one row each", withHistory.History)
	}

	// The restore puts them back when, and only when, the caller asks.
	restored, err := (ImportApplier{Store: store}).Apply(ctx, ImportRequest{
		Archive: withHistory, BankID: bank, TargetBankID: "hist-dst",
		Mode: ImportRestore, Scope: ExportScope{Data: true, BankConfig: true, History: true},
	})
	if err != nil {
		t.Fatalf("import with history: %v", err)
	}
	if restored.HistoryRows != 2 {
		t.Fatalf("history rows imported = %d, want the audit row and the model-request row", restored.HistoryRows)
	}
	audit, err := store.ListAuditLog(ctx, storage.AuditQuery{BankID: "hist-dst", Limit: 10})
	if err != nil || len(audit.Entries) != 1 {
		t.Fatalf("audit rows in the target = %v (err %v), want 1", audit.Entries, err)
	}
}

// test-import-reads-the-reference-json-encoding: the reference declares
// `bank_rows_json_encoding: "serialized"` and writes every jsonb column as its
// JSON text, so an archive it produced carries `"{\"mode\": \"delta\"}"` where
// this service writes the object. Reading only the object form loses the field
// silently: a bank's config, a mental model's trigger and a history payload all
// fall back to their defaults.
func TestRestoreReadsSerializedRowJSON(t *testing.T) {
	ctx := context.Background()
	archive := &hindsightarchive.Archive{
		Manifest: hindsightarchive.Manifest{
			SchemaVersion: hindsightarchive.SupportedSchemaVersion,
			SourceBankID:  "src",
			ArchiveType:   "bank",
		},
		Banks: []map[string]any{{
			"bank_id":     "src",
			"name":        "source",
			"mission":     "keep the ledger",
			"disposition": `{"empathy": 4, "literalism": 2, "skepticism": 5}`,
			"config":      `{"retain_extraction_mode": "verbose", "entity_labels": [{"key": "knowledge"}]}`,
		}},
		MentalModels: []map[string]any{{
			"id":           "mm-1",
			"bank_id":      "src",
			"name":         "ledger",
			"trigger":      `{"mode": "delta", "fact_types": ["world"], "refresh_after_consolidation": true}`,
			"observations": `{"observations": [{"text": "seen"}]}`,
		}},
		MentalModelHistory: []map[string]any{{
			"mental_model_id": "mm-1",
			"bank_id":         "src",
			"changed_at":      "2026-01-02T00:00:00Z",
			"content":         `{"previous_content": "the version before", "previous_reflect_response": {"based_on": {}}}`,
		}},
	}
	store := storage.NewMemoryStore("public")
	report, err := (ImportApplier{Store: store}).Apply(ctx, ImportRequest{
		Archive: archive, BankID: "src", TargetBankID: "dst",
		Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.MentalModelHistory != 1 {
		t.Fatalf("history rows imported = %d, want 1", report.MentalModelHistory)
	}

	bank, err := store.GetBank(ctx, "dst")
	if err != nil {
		t.Fatalf("bank: %v", err)
	}
	if got, _ := bank.Config["retain_extraction_mode"].(string); got != "verbose" {
		t.Fatalf("bank config = %v, want the serialized value", bank.Config)
	}
	if bank.Disposition["empathy"] != 4 || bank.Disposition["skepticism"] != 5 {
		t.Fatalf("disposition = %v, want the serialized values", bank.Disposition)
	}
	if bank.Mission != "keep the ledger" {
		t.Fatalf("mission = %q, want the plain text column kept literal", bank.Mission)
	}

	model, err := store.GetMentalModel(ctx, "dst", "mm-1")
	if err != nil {
		t.Fatalf("mental model: %v", err)
	}
	if mode, _ := model.Trigger["mode"].(string); mode != "delta" {
		t.Fatalf("trigger = %v, want the serialized trigger", model.Trigger)
	}
	if facts, _ := model.Trigger["fact_types"].([]any); len(facts) != 1 {
		t.Fatalf("trigger fact_types = %v, want the serialized list", model.Trigger["fact_types"])
	}

	history, err := store.MentalModelHistory(ctx, "dst", "mm-1")
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %v (err %v)", history, err)
	}
	if history[0].PreviousContent == nil || *history[0].PreviousContent != "the version before" {
		t.Fatalf("history previous_content = %v, want the serialized payload", history[0].PreviousContent)
	}
}

// A restore re-points the carried maintenance queues onto the target's own
// units/entities and counts what landed. A graph row naming a source unit the
// replay produced is remapped; a row naming a unit that did not come back is
// dropped.
func TestRestoreRemapsMaintenanceQueues(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, facts := seedSourceBank(t, source)
	archive, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatal(err)
	}
	// One row names a fact that travels, one names a unit that does not.
	archive.Data[hindsightarchive.GraphMaintenanceQueueTable] = []map[string]any{
		{"bank_id": bank, "unit_id": facts["Alice moved to Berlin | Involving: Alice"]},
		{"bank_id": bank, "unit_id": "00000000-0000-4000-8000-000000000000"},
	}
	target := storage.NewMemoryStore("public")
	report, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, BankID: bank, TargetBankID: "copy", Mode: ImportRestore,
		Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.MaintenanceQueueRows != 1 {
		t.Fatalf("maintenance rows imported = %d, want 1 (the mapped row only)", report.MaintenanceQueueRows)
	}
	drained, err := target.DrainGraphMaintenance(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	sourceUnitID := facts["Alice moved to Berlin | Involving: Alice"]
	ordinal := -1
	for index, fact := range archive.Documents[0].Facts {
		if fact.SourceID == sourceUnitID {
			ordinal = index
		}
	}
	if ordinal < 0 {
		t.Fatal("the named fact must travel in the archive")
	}
	want := archiveUnitID("copy", "d1", ordinal)
	found := false
	for _, row := range drained {
		if row.BankID == "copy" && row.ID == want {
			found = true
		}
		if row.ID == "00000000-0000-4000-8000-000000000000" {
			t.Fatalf("a row naming an absent unit must be dropped")
		}
	}
	if !found {
		t.Fatalf("the mapped graph row must be queued for the target: %+v", drained)
	}
}
