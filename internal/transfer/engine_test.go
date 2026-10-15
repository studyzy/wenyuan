package transfer

import (
	"context"
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

// An attachment whose document did not come across is skipped on import: its
// row would name a document the target does not hold.
func TestImportSkipsAttachmentsOfUnimportedDocuments(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	seedExportAttachment(t, source, bank, "d1")

	archive, err := BuildArchive(ctx, source, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// A merge that lands a different document leaves the attachment's own
	// document unimported.
	archive.Attachments[0].DocumentID = "not-imported"

	target := storage.NewMemoryStore("public")
	report, err := ImportApplier{Store: target}.Apply(ctx, ImportRequest{
		Archive: archive, BankID: "dst", Mode: ImportMerge,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if report.AttachmentsImported != 0 || report.AttachmentsSkipped != 1 {
		t.Fatalf("report = %+v", report)
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

// The operations log travels with the data section, and only its finished rows:
// a pending operation names work the source instance still owes.
func TestFinishedOperationsTravelOnly(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, source)
	if _, err := source.PutOperation(ctx, storage.Operation{
		OperationID: "op-done", BankID: bank, OperationType: "retain",
		Status: storage.StatusCompleted, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
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
	if len(rows) != 1 || rows[0]["operation_type"] != "retain" {
		t.Fatalf("operation rows = %+v", rows)
	}
	if _, carried := rows[0]["bank_id"]; !carried {
		t.Fatalf("row = %+v", rows[0])
	}
	if archive.Manifest.OperationCount != 1 {
		t.Fatalf("manifest operation count = %d", archive.Manifest.OperationCount)
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
	if page.Operations[0].Status != storage.StatusCompleted || page.Operations[0].OperationID == "op-done" {
		t.Fatalf("restored operation = %+v", page.Operations[0])
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
