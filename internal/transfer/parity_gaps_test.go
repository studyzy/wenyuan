package transfer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// test-restore-validates-resolved-target: a restore validates the resolved
// target bank identifier — including one derived from the archive's manifest —
// before the operation is queued, so a reserved prefix answers the reference's
// 400 now instead of creating the bank in the background
// (engine/memory_engine.py:8240, engine/retain/bank_utils.py:353-356).
func TestRestoreRejectsReservedTargetBankID(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "src")
	data := archiveBytes(t, "src")

	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/src/transfer/import?target_bank_id=_x", body,
		map[string]string{"bank_id": "src"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Bank id must not start with '_': that prefix is reserved") {
		t.Fatalf("body = %s, want the reference's reserved-prefix sentence", rec.Body.String())
	}
	if _, err := store.GetBank(context.Background(), "_x"); err == nil {
		t.Fatal("the reserved target bank must not have been created")
	}
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{BankID: "src"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("a rejected restore must queue nothing, got %d operations", len(page.Operations))
	}

	// A target that violates no rule is still accepted.
	body, contentType = multipartArchive(t, data)
	rec = httptest.NewRecorder()
	req = routeRequest(http.MethodPost, "/v1/default/banks/src/transfer/import?target_bank_id=dst", body,
		map[string]string{"bank_id": "src"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
}

// The manifest-derived target is validated the same way: an archive whose
// source bank id is reserved must not restore into it either.
func TestRestoreRejectsReservedManifestSourceBankID(t *testing.T) {
	h, store := newTestHandlers(t)
	seedBank(t, store, "src")
	data := archiveBytes(t, "_src")

	body, contentType := multipartArchive(t, data)
	rec := httptest.NewRecorder()
	req := routeRequest(http.MethodPost, "/v1/default/banks/src/transfer/import", body,
		map[string]string{"bank_id": "src"})
	req.Header.Set("Content-Type", contentType)
	h.SubmitTransferImport(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Bank id must not start with '_': that prefix is reserved") {
		t.Fatalf("body = %s, want the reference's reserved-prefix sentence", rec.Body.String())
	}
	if _, err := store.GetBank(context.Background(), "_src"); err == nil {
		t.Fatal("the reserved manifest source bank must not have been created")
	}
}

// seedEvidenceModel writes a mental model whose reflect response and whose
// history's previous reflect response ground themselves in the seeded facts.
func seedEvidenceModel(t *testing.T, store *storage.MemoryStore, bank, factID string) {
	t.Helper()
	ctx := context.Background()
	model := storage.MentalModel{
		ID:        "mm-1",
		BankID:    bank,
		Subtype:   storage.SubtypePinned,
		Name:      "alice",
		Content:   "Alice lives in Berlin",
		MaxTokens: 512,
		ReflectResult: map[string]any{
			"based_on": map[string]any{
				"memory_ids": []any{
					map[string]any{"id": factID, "text": "Alice moved to Berlin"},
				},
				"mental_model_ids": []any{
					map[string]any{"id": "mm-2"},
				},
			},
		},
	}
	if _, err := store.PutMentalModel(ctx, model); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordMentalModelHistory(ctx, bank, "mm-1", storage.MentalModelHistoryEntry{
		ChangedAt: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		PreviousReflectResponse: map[string]any{
			"based_on": map[string]any{
				"memory_ids": []any{
					map[string]any{"id": factID},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
}

// basedOnEntry reads one evidence entry back out of a restored reflect
// response.
func basedOnEntry(t *testing.T, response map[string]any, group string, index int) map[string]any {
	t.Helper()
	basedOn, ok := response["based_on"].(map[string]any)
	if !ok {
		t.Fatalf("reflect response has no based_on object: %+v", response)
	}
	entries, ok := basedOn[group].([]any)
	if !ok || len(entries) <= index {
		t.Fatalf("based_on[%s] = %#v, want at least %d entries", group, basedOn[group], index+1)
	}
	entry, ok := entries[index].(map[string]any)
	if !ok {
		t.Fatalf("based_on[%s][%d] = %#v, want an object", group, index, entries[index])
	}
	return entry
}

// Facts and observations are replayed under fresh ids while mental-model rows
// keep theirs, so the models' grounded evidence — current and historical — is
// re-pointed at the target's own units before anything is written
// (engine/transfer/importer.py:2024-2075, :1393-1394).
func TestRestoreRemapsMentalModelEvidence(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	bank, ids := seedSourceBank(t, store)
	factID := ids["Alice moved to Berlin | Involving: Alice"]
	seedEvidenceModel(t, store, bank, factID)

	archive, err := BuildArchive(ctx, store, bank, ExportOptions{Scope: DefaultExportScope(), IncludeKnowledgeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	// The evidence travels as the reference's serialized jsonb text, so the
	// rewrite has to read it back out of the string form.
	reflectText, ok := archive.MentalModels[0]["reflect_response"].(string)
	if !ok || !strings.Contains(reflectText, factID) {
		t.Fatalf("reflect_response = %#v, want serialized JSON carrying the source id", archive.MentalModels[0]["reflect_response"])
	}

	report, err := ImportApplier{Store: store, Embeddings: &fixedEmbeddings{}}.Apply(ctx, ImportRequest{
		Archive: archive, BankID: bank, TargetBankID: "dst", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.MentalModelsImported != 1 || report.MentalModelHistory != 1 {
		t.Fatalf("report = %+v", report)
	}
	imported, err := store.GetMentalModel(ctx, "dst", "mm-1")
	if err != nil {
		t.Fatal(err)
	}
	// The fact's target id is the id the import minted for its ordinal in the
	// archive, so it is read off the archive rather than assumed.
	want := ""
	for ordinal, fact := range archive.Documents[0].Facts {
		if fact.SourceID == factID {
			want = archiveUnitID("dst", "d1", ordinal)
		}
	}
	if want == "" {
		t.Fatalf("the archive carries no fact with source id %s: %+v", factID, archive.Documents[0].Facts)
	}
	entry := basedOnEntry(t, imported.ReflectResult, "memory_ids", 0)
	if entry["id"] != want {
		t.Fatalf("based_on.memory_ids[0].id = %v, want the target's unit %s", entry["id"], want)
	}
	// A model keeps its id, so a mental-model evidence entry has no mapping and
	// is carried verbatim.
	entry = basedOnEntry(t, imported.ReflectResult, "mental_model_ids", 0)
	if entry["id"] != "mm-2" {
		t.Fatalf("based_on.mental_model_ids[0].id = %v, want the carried mm-2", entry["id"])
	}
	entries, err := store.MentalModelHistory(ctx, "dst", "mm-1")
	if err != nil || len(entries) != 1 {
		t.Fatalf("history = %+v, err = %v", entries, err)
	}
	entry = basedOnEntry(t, entries[0].PreviousReflectResponse, "memory_ids", 0)
	if entry["id"] != want {
		t.Fatalf("history previous_reflect_response id = %v, want the target's unit %s", entry["id"], want)
	}
}

// seedPageBackedModel writes a standalone model, a page-backed model and the
// page that backs it, which is the shape the standalone count is defined on.
func seedPageBackedModel(t *testing.T, store *storage.MemoryStore, bank string) {
	t.Helper()
	ctx := context.Background()
	for _, model := range []storage.MentalModel{
		{ID: "mm-standalone", BankID: bank, Subtype: storage.SubtypePinned, Name: "standalone", Content: "a note"},
		{ID: "mm-page", BankID: bank, Subtype: storage.SubtypePinned, Name: "page", Content: "page content"},
	} {
		if _, err := store.PutMentalModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	node := storage.KnowledgeNode{
		ID:            "page-1",
		BankID:        bank,
		Kind:          storage.KnowledgeKindPage,
		Title:         "Deployment",
		MentalModelID: "mm-page",
	}
	if _, err := store.PutKnowledgeNode(ctx, node); err != nil {
		t.Fatal(err)
	}
}

// A page keeps its content in a mental_models row: the row travels with the
// archive, but the manifest and the import report count it as a knowledge
// page, not as a mental model (engine/transfer/importer.py:624-644,
// engine/transfer/export.py:357, :662, :1007).
func TestKnowledgePageBackedModelsAreCountedAsPages(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, store)
	seedPageBackedModel(t, store, bank)

	archive, err := BuildArchive(ctx, store, bank, ExportOptions{Scope: DefaultExportScope(), IncludeKnowledgeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if archive.Manifest.MentalModelCount != 1 {
		t.Fatalf("manifest mental_model_count = %d, want the one standalone model", archive.Manifest.MentalModelCount)
	}
	if len(archive.MentalModels) != 2 {
		t.Fatalf("carried mental_models = %d, want both rows (a page-backed row still travels)", len(archive.MentalModels))
	}
	if archive.Manifest.KnowledgePageCount != 1 {
		t.Fatalf("manifest knowledge_page_count = %d, want 1", archive.Manifest.KnowledgePageCount)
	}

	report, err := ImportApplier{Store: store, Embeddings: &fixedEmbeddings{}}.Apply(ctx, ImportRequest{
		Archive: archive, BankID: bank, TargetBankID: "dst", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.MentalModelsImported != 1 {
		t.Fatalf("mental_models_imported = %d, want the one standalone model", report.MentalModelsImported)
	}
	if report.KnowledgePagesImported != 1 {
		t.Fatalf("knowledge_pages_imported = %d, want 1", report.KnowledgePagesImported)
	}
	// Both rows landed: the page-backed one is only counted differently.
	for _, id := range []string{"mm-standalone", "mm-page"} {
		if _, err := store.GetMentalModel(ctx, "dst", id); err != nil {
			t.Fatalf("mental model %s did not arrive: %v", id, err)
		}
	}
}

// stampFactLifecycle records when the seeded facts were written and
// consolidated, which is the state the export's lifecycle gating decides on.
func stampFactLifecycle(t *testing.T, store *storage.MemoryStore, bank string) {
	t.Helper()
	ctx := context.Background()
	page, err := store.ListMemories(ctx, storage.MemoryQuery{BankID: bank, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	consolidated := created.Add(time.Minute)
	failed := created.Add(2 * time.Minute)
	for _, unit := range page.Memories {
		if unit.FactType == storage.TypeObservation {
			continue
		}
		unit.CreatedAt = &created
		unit.ConsolidatedAt = &consolidated
		unit.ConsolidationFailedAt = &failed
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
}

// The per-fact consolidation lifecycle travels exactly when the observations
// do: a document export without them drops it so the target re-consolidates
// from scratch, a document export with them keeps it, and a whole-bank export
// always carries it. A failed consolidation is never exported — the reference's
// payload has no member for it (engine/transfer/export.py:319, :588,
// :1239-1241).
func TestExportCarriesFactLifecycleOnlyWithObservations(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	bank, _ := seedSourceBank(t, store)
	stampFactLifecycle(t, store, bank)

	without, err := BuildArchive(ctx, store, bank, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fact := without.Documents[0].Facts[0]
	if fact.CreatedAt != nil || fact.ConsolidatedAt != nil {
		t.Fatalf("a document export without observations must drop the lifecycle, got %v/%v", fact.CreatedAt, fact.ConsolidatedAt)
	}
	with, err := BuildArchive(ctx, store, bank, ExportOptions{IncludeObservations: true})
	if err != nil {
		t.Fatal(err)
	}
	fact = with.Documents[0].Facts[0]
	if fact.CreatedAt == nil || fact.ConsolidatedAt == nil {
		t.Fatalf("a document export with observations must carry the lifecycle, got %v/%v", fact.CreatedAt, fact.ConsolidatedAt)
	}
	if fact.ConsolidationFailedAt != nil {
		t.Fatalf("consolidation_failed_at must export as unset, got %v", fact.ConsolidationFailedAt)
	}
	wholeBank, err := BuildArchive(ctx, store, bank, ExportOptions{Scope: DefaultExportScope()})
	if err != nil {
		t.Fatal(err)
	}
	fact = wholeBank.Documents[0].Facts[0]
	if fact.CreatedAt == nil || fact.ConsolidatedAt == nil {
		t.Fatalf("a whole-bank export always carries the lifecycle, got %v/%v", fact.CreatedAt, fact.ConsolidatedAt)
	}
	if fact.ConsolidationFailedAt != nil {
		t.Fatalf("consolidation_failed_at must export as unset, got %v", fact.ConsolidationFailedAt)
	}
}

// An observation with no sources, or one whose sources are not all in the
// exported set, is skipped whole rather than carried with a truncated evidence
// list, because the importing bank would resolve the missing ones to nothing
// (engine/transfer/export.py:1300-1305).
func TestExportSkipsAnObservationWithAnUnmappedSource(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	bank, ids := seedSourceBank(t, store)

	// An export that carries only the first fact leaves the observation's second
	// source outside the set.
	locations := map[string]transferarchive.ObservationSource{
		ids["Alice moved to Berlin | Involving: Alice"]: {DocumentID: "d1", FactIndex: 0},
	}
	observations, err := buildObservations(ctx, store, bank, locations)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 0 {
		t.Fatalf("observations = %+v, want the whole observation skipped when a source is outside the export", observations)
	}

	// An observation with no sources at all is skipped the same way.
	orphan := storage.MemoryUnit{
		ID:       archiveObservationID(bank, 1, "orphan"),
		BankID:   bank,
		Text:     "nothing grounds this",
		FactType: storage.TypeObservation,
		Metadata: map[string]any{},
	}
	if _, err := store.UpsertUnit(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	observations, err = buildObservations(ctx, store, bank, locations)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 0 {
		t.Fatalf("observations = %+v, want the sourceless observation skipped", observations)
	}

	// With every source in the export the observation travels complete.
	archive, err := BuildArchive(ctx, store, bank, ExportOptions{IncludeObservations: true, Scope: DefaultExportScope()})
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Observations) != 1 || len(archive.Observations[0].Sources) != 2 {
		t.Fatalf("observations = %+v, want one observation over both of its sources", archive.Observations)
	}
}

// An archive from a bank that stored an open bound as infinity carries it as
// year 1 / year 9999; importing it as unknown rather than recreating the row is
// what the reference's validators do (engine/transfer/schema.py:176-177,
// :225-226).
func TestImportDropsPlaceholderFactDates(t *testing.T) {
	store := storage.NewMemoryStore("public")
	placeholder := "9999-12-31T23:59:59Z"
	real := "2026-02-03T04:05:06Z"
	archive := &transferarchive.Archive{
		Manifest: transferarchive.Manifest{
			SchemaVersion: transferarchive.SupportedSchemaVersion,
			SourceBankID:  "src",
			ArchiveType:   "bank",
		},
		Documents: []transferarchive.Document{{
			ID:           "d1",
			OriginalText: "text",
			Facts: []transferarchive.Fact{{
				Text:          "Alice moved to Berlin",
				SourceID:      "u1",
				FactType:      string(storage.TypeWorld),
				EventDate:     &placeholder,
				OccurredStart: &real,
			}},
		}},
	}
	report, err := ImportApplier{Store: store, Embeddings: &fixedEmbeddings{}}.Apply(context.Background(), ImportRequest{
		Archive: archive, BankID: "src", TargetBankID: "dst", Mode: ImportRestore, Scope: DefaultExportScope(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.FactsImported != 1 {
		t.Fatalf("report = %+v", report)
	}
	page, err := store.ListMemories(context.Background(), storage.MemoryQuery{BankID: "dst", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Memories) != 1 {
		t.Fatalf("memories = %+v", page.Memories)
	}
	unit := page.Memories[0]
	if unit.EventDate != nil {
		t.Fatalf("event_date = %v, want the placeholder bound dropped", unit.EventDate)
	}
	if unit.OccurredStart == nil || unit.OccurredStart.Year() != 2026 {
		t.Fatalf("occurred_start = %v, want the real date carried", unit.OccurredStart)
	}
}
