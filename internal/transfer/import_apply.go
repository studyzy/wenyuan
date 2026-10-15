package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ImportMode selects how an archive is applied to a bank.
type ImportMode string

const (
	// ImportMerge folds an archive's documents into an existing bank, leaving
	// whatever the bank already holds in place.
	ImportMerge ImportMode = "merge"
	// ImportRestore writes a whole bank into a fresh identifier: the bank is
	// created, and its configuration and synthesized state come across too.
	ImportRestore ImportMode = "restore"
)

// ErrTargetBankExists reports a restore whose target already holds a bank. A
// restore writes a whole bank, so merging it into a live one would mix two
// configurations silently; the caller is told immediately instead.
var ErrTargetBankExists = errors.New("the target bank already exists")

// Document conflict handling, matching the reference's on_conflict vocabulary.
const (
	ConflictSkip    = "skip"
	ConflictReplace = "replace"
	ConflictNewID   = "new-id"
)

// ImportRequest describes one archive application.
type ImportRequest struct {
	// Archive is the parsed archive to apply.
	Archive *hindsightarchive.Archive
	// BankID is the bank the operation is recorded against. In restore mode it
	// names an existing bank; in merge mode it is the merge target.
	BankID string
	// TargetBankID is the restore target. Empty means the archive's own source
	// bank.
	TargetBankID string
	// Mode is merge or restore.
	Mode ImportMode
	// OnConflict decides what happens to a document id that already exists in
	// the target. Empty means skip.
	OnConflict string
	// Scope narrows a restore to a subset of what the archive carries. It is
	// ignored in merge mode, which takes documents only.
	Scope ExportScope
}

// ImportReport is the observable outcome of applying one archive.
type ImportReport struct {
	TargetBankID           string
	DocumentsImported      int
	DocumentsSkipped       int
	FactsImported          int
	ObservationsImported   int
	MentalModelsImported   int
	KnowledgePagesImported int
	DirectivesImported     int
	WebhooksImported       int
	AttachmentsImported    int
	AttachmentsSkipped     int
	MentalModelHistory     int
	HistoryRows            int
	InvalidatedMemories    int
	Operations             int
}

// Metadata renders the report in the reference's result_metadata vocabulary,
// so a caller polling the operation reads the same keys it would anywhere else.
func (r ImportReport) Metadata() map[string]any {
	return map[string]any{
		"target_bank_id":                r.TargetBankID,
		"documents_imported":            r.DocumentsImported,
		"documents_skipped":             r.DocumentsSkipped,
		"facts_imported":                r.FactsImported,
		"observations_imported":         r.ObservationsImported,
		"mental_models_imported":        r.MentalModelsImported,
		"knowledge_pages_imported":      r.KnowledgePagesImported,
		"directives_imported":           r.DirectivesImported,
		"webhooks_imported":             r.WebhooksImported,
		"attachments_imported":          r.AttachmentsImported,
		"attachments_skipped":           r.AttachmentsSkipped,
		"mental_model_history_imported": r.MentalModelHistory,
		"history_rows_imported":         r.HistoryRows,
		"invalidated_memories_imported": r.InvalidatedMemories,
		"operations_imported":           r.Operations,
	}
}

// ImportApplier applies archives to a store. The embedding generator is the
// importing instance's own, because an archive carries no vectors: a fact's
// embedding is regenerated here so the vector arm and its index have vectors to
// rank.
type ImportApplier struct {
	Store      storage.Store
	Embeddings storage.EmbeddingGenerator
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// Apply writes the archive into its target bank and reports what it did.
func (a ImportApplier) Apply(ctx context.Context, req ImportRequest) (ImportReport, error) {
	if req.Archive == nil {
		return ImportReport{}, errors.New("no archive to import")
	}
	conflict := req.OnConflict
	if conflict == "" {
		conflict = ConflictSkip
	}
	switch conflict {
	case ConflictSkip, ConflictReplace, ConflictNewID:
	default:
		return ImportReport{}, fmt.Errorf("Invalid on_conflict '%s'; expected skip|replace|new-id", conflict) //nolint:staticcheck // exact copy text of the reference contract
	}
	report := ImportReport{}
	switch req.Mode {
	case ImportRestore:
		return a.restore(ctx, req, report)
	case ImportMerge, "":
		return a.merge(ctx, req, conflict, report)
	default:
		return report, fmt.Errorf("Invalid mode '%s' (expected restore|merge)", req.Mode) //nolint:staticcheck // exact copy text of the reference contract
	}
}

// restore writes a whole bank into a fresh identifier.
func (a ImportApplier) restore(ctx context.Context, req ImportRequest, report ImportReport) (ImportReport, error) {
	target := req.TargetBankID
	if target == "" {
		target = req.Archive.Manifest.SourceBankID
	}
	if target == "" {
		return report, errors.New("the archive names no source bank and no target was given")
	}
	if _, err := a.Store.GetBank(ctx, target); err == nil {
		return report, ErrTargetBankExists
	}
	report.TargetBankID = target
	if err := a.createBank(ctx, req, target); err != nil {
		return report, err
	}
	if req.Scope.Data {
		documentIDs, err := a.applyDocuments(ctx, req, target, ConflictSkip, &report)
		if err != nil {
			return report, err
		}
		if err := a.applyAttachments(ctx, req, target, documentIDs, &report); err != nil {
			return report, err
		}
		if err := a.applyObservations(ctx, req, target, documentIDs, &report); err != nil {
			return report, err
		}
		// Mental models and knowledge pages are a reading of the bank's facts,
		// so they ride with the data section rather than the configuration.
		if err := a.applyKnowledgeBase(ctx, req, target, &report); err != nil {
			return report, err
		}
		// The operations log and the curation archive are data too, so they are
		// restored only when the caller asked for the data section.
		if err := a.applyOperations(ctx, req, target, &report); err != nil {
			return report, err
		}
		if err := a.applyInvalidatedMemories(ctx, req, target, &report); err != nil {
			return report, err
		}
	}
	if req.Scope.BankConfig {
		if err := a.applyBankConfig(ctx, req, target, &report); err != nil {
			return report, err
		}
	}
	if err := a.applyOperationalHistory(ctx, req, target, &report); err != nil {
		return report, err
	}
	return report, nil
}

// merge folds an archive's documents into an existing bank, which is created
// when it does not exist yet so a first import into a fresh identifier works.
func (a ImportApplier) merge(ctx context.Context, req ImportRequest, conflict string, report ImportReport) (ImportReport, error) {
	target := req.BankID
	if target == "" {
		target = req.Archive.Manifest.SourceBankID
	}
	report.TargetBankID = target
	if err := a.Store.EnsureBank(ctx, target); err != nil {
		return report, err
	}
	documentIDs, err := a.applyDocuments(ctx, req, target, conflict, &report)
	if err != nil {
		return report, err
	}
	if err := a.applyAttachments(ctx, req, target, documentIDs, &report); err != nil {
		return report, err
	}
	if err := a.applyObservations(ctx, req, target, documentIDs, &report); err != nil {
		return report, err
	}
	if err := a.applyKnowledgeBase(ctx, req, target, &report); err != nil {
		return report, err
	}
	return report, a.applyOperationalHistory(ctx, req, target, &report)
}

// applyAttachments writes each carried attachment's bytes and its row into the
// target, under a storage key recomputed for the target bank.
//
// An attachment whose document did not come across is skipped: its row would
// name a document the target does not hold, which no reader could resolve.
func (a ImportApplier) applyAttachments(ctx context.Context, req ImportRequest, target string, documentIDs map[string]string, report *ImportReport) error {
	for _, attachment := range req.Archive.Attachments {
		documentID := documentIDs[attachment.DocumentID]
		if documentID == "" {
			report.AttachmentsSkipped++
			continue
		}
		data := req.Archive.Blobs[attachment.Entry]
		key := storage.AttachmentStorageKey(target, documentID, attachment.AttachmentHash)
		if err := a.Store.PutStoredFile(ctx, storage.StoredFile{
			Key:         key,
			ContentType: attachment.MediaType,
			Data:        data,
		}); err != nil {
			return err
		}
		if err := a.Store.PutAttachment(ctx, storage.Attachment{
			BankID:         target,
			DocumentID:     documentID,
			AttachmentHash: attachment.AttachmentHash,
			ShortID:        attachment.ShortID,
			MediaType:      attachment.MediaType,
			ByteSize:       int64(len(data)),
			StorageKey:     key,
			Kind:           attachment.Kind,
			Filename:       attachment.Filename,
		}); err != nil {
			return err
		}
		report.AttachmentsImported++
	}
	return nil
}

// createBank creates the restore target, carrying the archive's own bank row
// when the archive holds one and the configuration section was requested.
func (a ImportApplier) createBank(ctx context.Context, req ImportRequest, target string) error {
	bank := storage.Bank{
		BankID:      target,
		Name:        target,
		Disposition: map[string]int{},
		CreatedAt:   a.now(),
		UpdatedAt:   a.now(),
	}
	if len(req.Archive.Banks) > 0 {
		row := req.Archive.Banks[0]
		if name := rowString(row, "name"); name != "" {
			bank.Name = name
		}
		if mission := rowString(row, "mission"); mission != "" {
			bank.Mission = mission
		}
		if disposition := rowIntMap(row, "disposition"); len(disposition) > 0 {
			bank.Disposition = disposition
		}
		if config, ok := rowMap(row, "config"); ok {
			bank.Config = config
		}
	}
	_, err := a.Store.CreateBank(ctx, bank)
	return err
}

// applyDocuments replays every document payload into the target bank: the
// document row, its facts re-embedded with the target's model, the entity
// resolution the reference re-runs, and the causal links between a document's
// own facts.
func (a ImportApplier) applyDocuments(ctx context.Context, req ImportRequest, target, conflict string, report *ImportReport) (map[string]string, error) {
	documentIDs := make(map[string]string, len(req.Archive.Documents))
	for _, document := range req.Archive.Documents {
		documentID := document.ID
		if _, err := a.Store.GetDocument(ctx, target, documentID); err == nil {
			switch conflict {
			case ConflictSkip:
				report.DocumentsSkipped++
				// A skipped document keeps the identity the target already
				// holds, which is what its facts are addressed by.
				documentIDs[document.ID] = documentID
				continue
			case ConflictReplace:
				if _, err := a.Store.DeleteDocument(ctx, target, documentID); err != nil {
					return nil, fmt.Errorf("replace document %s: %w", documentID, err)
				}
			case ConflictNewID:
				documentID = archiveDocumentID(target, document.ID)
			}
		}
		if err := a.writeDocument(ctx, target, documentID, document); err != nil {
			return nil, err
		}
		documentIDs[document.ID] = documentID
		report.DocumentsImported++
		report.FactsImported += len(document.Facts)
	}
	return documentIDs, nil
}

// writeDocument persists one document and its facts.
func (a ImportApplier) writeDocument(ctx context.Context, target, documentID string, document hindsightarchive.Document) error {
	created := a.now()
	if document.CreatedAt != nil {
		created = document.CreatedAt.UTC()
	}
	stored, err := a.Store.PutDocument(ctx, target, documentID, storage.Document{
		ID:           documentID,
		OriginalText: document.OriginalText,
		RetainParams: document.RetainParams,
		Tags:         nonNilStrings(document.Tags),
		CreatedAt:    created,
	})
	if err != nil {
		return fmt.Errorf("write document %s: %w", documentID, err)
	}
	_ = stored

	units := make([]storage.MemoryUnit, 0, len(document.Facts))
	for ordinal, fact := range document.Facts {
		units = append(units, a.unitForFact(target, documentID, document, ordinal, fact))
	}
	if err := a.embedUnits(ctx, units); err != nil {
		return err
	}
	for index, unit := range units {
		fact := document.Facts[index]
		if _, err := a.Store.UpsertUnit(ctx, unit); err != nil {
			return fmt.Errorf("write fact %d of %s: %w", index, documentID, err)
		}
		for _, name := range fact.Entities {
			entity, err := a.Store.ResolveEntity(ctx, target, name, storage.EntityKindRegular, true)
			if err != nil {
				return fmt.Errorf("resolve entity %q: %w", name, err)
			}
			if err := a.Store.UpsertUnitEntity(ctx, unit.ID, entity.ID); err != nil {
				return fmt.Errorf("link unit %s to entity %s: %w", unit.ID, entity.ID, err)
			}
		}
		for _, relation := range fact.CausalRelations {
			if relation.TargetFactIndex < 0 || relation.TargetFactIndex >= len(document.Facts) {
				continue
			}
			link := storage.MemoryLink{
				BankID:     target,
				FromUnitID: unit.ID,
				ToUnitID:   archiveUnitID(target, documentID, relation.TargetFactIndex),
				LinkType:   storage.MemoryLinkType(relation.RelationType),
				Weight:     1,
			}
			if err := a.Store.UpsertLink(ctx, link); err != nil {
				return fmt.Errorf("write causal link from %s: %w", unit.ID, err)
			}
		}
	}
	return nil
}

// unitForFact converts one archive fact into the target bank's memory unit.
func (a ImportApplier) unitForFact(target, documentID string, document hindsightarchive.Document, ordinal int, fact hindsightarchive.Fact) storage.MemoryUnit {
	unit := storage.MemoryUnit{
		ID:                    archiveUnitID(target, documentID, ordinal),
		BankID:                target,
		Text:                  fact.Text,
		FactType:              storage.MemoryUnitType(fact.FactType),
		Context:               fact.Context,
		EventDate:             fact.EventDate,
		OccurredStart:         fact.OccurredStart,
		OccurredEnd:           fact.OccurredEnd,
		MentionedAt:           fact.MentionedAt,
		Metadata:              fact.Metadata,
		DocumentID:            documentID,
		Tags:                  nonNilStrings(fact.Tags),
		ConsolidatedAt:        fact.ConsolidatedAt,
		ConsolidationFailedAt: fact.ConsolidationFailedAt,
		CreatedAt:             fact.CreatedAt,
	}
	if unit.Metadata == nil {
		unit.Metadata = map[string]any{}
	}
	// A fact's chunk id is deliberately not carried: the archive's chunk rows
	// are informational, the store's write path is what creates chunk rows, and
	// a unit naming a chunk this import did not write would violate the chunk
	// foreign key. The fact's text and its document are what recall reads.
	unit.ObservationScopes = factObservationScopes(fact)
	return unit
}

// applyObservations writes the archive's observations, rewriting each one's
// evidence onto the facts this import actually inserted. An observation whose
// sources do not all resolve is skipped rather than stored with dangling
// evidence, which is the failure this rewrite exists to prevent.
func (a ImportApplier) applyObservations(ctx context.Context, req ImportRequest, target string, documentIDs map[string]string, report *ImportReport) error {
	if len(req.Archive.Observations) == 0 {
		return nil
	}
	locations := unitLocations(target, req.Archive, documentIDs)
	units := make([]storage.MemoryUnit, 0, len(req.Archive.Observations))
	for index, observation := range req.Archive.Observations {
		sources := make([]string, 0, len(observation.Sources))
		for _, source := range observation.Sources {
			unitID, ok := locations[observationKey(source.DocumentID, source.FactIndex)]
			if !ok {
				sources = nil
				break
			}
			sources = append(sources, unitID)
		}
		if len(sources) == 0 {
			continue
		}
		unit := storage.MemoryUnit{
			ID:                archiveObservationID(target, index, observation.SourceID),
			BankID:            target,
			Text:              observation.Text,
			FactType:          storage.TypeObservation,
			Tags:              nonNilStrings(observation.Tags),
			SourceMemoryIDs:   sources,
			ProofCount:        proofCount(observation.ProofCount, len(sources)),
			EventDate:         observation.EventDate,
			OccurredStart:     observation.OccurredStart,
			OccurredEnd:       observation.OccurredEnd,
			MentionedAt:       observation.MentionedAt,
			CreatedAt:         observation.CreatedAt,
			ObservationScopes: observation.ObservationScopes,
			Metadata:          map[string]any{},
		}
		units = append(units, unit)
	}
	if err := a.embedUnits(ctx, units); err != nil {
		return err
	}
	for _, unit := range units {
		if _, err := a.Store.UpsertUnit(ctx, unit); err != nil {
			return fmt.Errorf("write observation %s: %w", unit.ID, err)
		}
		report.ObservationsImported++
	}
	return nil
}

// unitLocations maps every (document, ordinal) in the archive to the unit id
// this import assigned it, which is what an observation's rewritten evidence
// resolves through. The document's *imported* id is used, so a document that
// arrived under a new identity still resolves its own facts.
func unitLocations(target string, archive *hindsightarchive.Archive, documentIDs map[string]string) map[string]string {
	locations := map[string]string{}
	for _, document := range archive.Documents {
		documentID, ok := documentIDs[document.ID]
		if !ok {
			documentID = document.ID
		}
		for ordinal := range document.Facts {
			locations[observationKey(document.ID, ordinal)] = archiveUnitID(target, documentID, ordinal)
		}
	}
	return locations
}

// applyKnowledgeBase restores the archive's mental models and knowledge-page
// tree, parent-first so every parent link resolves. Both keep their archive
// identifiers, because a page names its backing model and every node names its
// parent.
func (a ImportApplier) applyKnowledgeBase(ctx context.Context, req ImportRequest, target string, report *ImportReport) error {
	if len(req.Archive.KnowledgePages) > 0 && len(req.Archive.MentalModels) == 0 {
		return errors.New("Knowledge Pages require their backing mental models in the transfer archive") //nolint:staticcheck // exact copy text of the reference contract
	}
	for _, row := range req.Archive.MentalModels {
		model := mentalModelFromRow(row, target, a.now())
		if model.ID == "" {
			continue
		}
		if _, err := a.Store.PutMentalModel(ctx, model); err != nil {
			return fmt.Errorf("write mental model %s: %w", model.ID, err)
		}
		report.MentalModelsImported++
	}
	if err := a.applyMentalModelHistory(ctx, req, target, report); err != nil {
		return err
	}
	// The tree's node ids are reassigned per target bank: a node id is unique
	// across the whole schema, so carrying the source's id onto an instance that
	// still holds the source would collide with it and leave the copy reading a
	// node it does not own. Parent links are rewritten onto the new ids so the
	// hierarchy survives; a page's backing mental model keeps its id, because that
	// identity is unique per bank.
	pageIDs := make(map[string]string, len(req.Archive.KnowledgePages))
	for _, page := range req.Archive.KnowledgePages {
		pageIDs[page.ID] = stableArchiveID("page", target, page.ID)
	}
	for _, page := range orderKnowledgePages(req.Archive.KnowledgePages) {
		parentID := ""
		if page.ParentID != "" {
			parentID = pageIDs[page.ParentID]
		}
		node, err := a.Store.PutKnowledgeNode(ctx, storage.KnowledgeNode{
			ID:            pageIDs[page.ID],
			BankID:        target,
			ParentID:      parentID,
			Kind:          storage.KnowledgeKind(page.Kind),
			Title:         page.Name,
			MentalModelID: page.MentalModelID,
			Managed:       page.Managed,
			SortOrder:     page.SortOrder,
		})
		if err != nil {
			return fmt.Errorf("write knowledge page %s: %w", page.ID, err)
		}
		_ = node
		report.KnowledgePagesImported++
	}
	return nil
}

// applyOperations replays the archive's finished operations into the target's
// own log. The identifier is left to the store, so the target's sequence stays
// ahead and the log reads as the target's own.
func (a ImportApplier) applyOperations(ctx context.Context, req ImportRequest, target string, report *ImportReport) error {
	for _, row := range req.Archive.Data[hindsightarchive.AsyncOperationsTable] {
		status := storage.OperationStatus(rowString(row, "status"))
		if !status.Terminal() {
			// A row that is not finished would make the target's worker pick up
			// work belonging to another instance.
			continue
		}
		operation := storage.Operation{
			BankID:           target,
			OperationType:    rowString(row, "operation_type"),
			Status:           status,
			Error:            rowString(row, "error"),
			CreatedAt:        rowTime(row, "created_at", a.now()),
			UpdatedAt:        rowTime(row, "updated_at", a.now()),
			CompletedAt:      rowTimePtr(row, "completed_at"),
			NextRetryAt:      rowTimePtr(row, "next_retry_at"),
			RetryCount:       rowInt(row, "retry_count"),
			SerializationKey: rowString(row, "serialization_key"),
			IsParent:         rowBool(row, "is_parent"),
			ResultMetadata:   mapValue(row, "result_metadata"),
			Payload:          mapValue(row, "payload"),
		}
		if _, err := a.Store.PutOperation(ctx, operation); err != nil {
			return fmt.Errorf("write operation: %w", err)
		}
		report.Operations++
	}
	return nil
}

// applyInvalidatedMemories restores the curation archive into the backend that
// keeps one. The rows travel verbatim, so only the bank they belong to is
// rewritten; an id already archived is left alone, which makes a repeated
// restore idempotent. A backend without an archive is not an error: the rows
// have nowhere to go, and the bank itself restored fine.
func (a ImportApplier) applyInvalidatedMemories(ctx context.Context, req ImportRequest, target string, report *ImportReport) error {
	rows := req.Archive.Data[hindsightarchive.InvalidatedMemoryTable]
	if len(rows) == 0 {
		return nil
	}
	archived, ok := a.Store.(storage.ArchivedMemoryStore)
	if !ok {
		return nil
	}
	for _, row := range rows {
		restored := make(map[string]any, len(row))
		for key, value := range row {
			restored[key] = value
		}
		restored["bank_id"] = target
		inserted, err := archived.PutArchivedMemory(ctx, restored)
		if err != nil {
			return fmt.Errorf("write archived memory: %w", err)
		}
		if inserted {
			report.InvalidatedMemories++
		}
		// A row already archived under the same id is not an error: an archived
		// unit keeps its memory id, and a same-instance copy finds the source's
		// row already holding it. The count reports only what landed.
		_ = inserted
	}
	return nil
}

// applyMentalModelHistory replays each carried refresh log onto the model it
// belongs to. It runs after the models exist, because the row's foreign key
// names one, and it leaves the surrogate row id to the target: the archive
// dropped it so the target's own identity sequence stays ahead.
func (a ImportApplier) applyMentalModelHistory(ctx context.Context, req ImportRequest, target string, report *ImportReport) error {
	for _, row := range req.Archive.MentalModelHistory {
		modelID := rowString(row, "mental_model_id")
		if modelID == "" {
			continue
		}
		content, _ := rowMap(row, "content")
		entry := storage.MentalModelHistoryEntry{
			ChangedAt:               rowTime(row, "changed_at", a.now()),
			PreviousReflectResponse: mapValue(content, "previous_reflect_response"),
			Kind:                    mapString(content, "kind"),
			Outcome:                 mapString(content, "outcome"),
			FailureReason:           mapString(content, "failure_reason"),
			ErrorMessage:            mapString(content, "error_message"),
		}
		if previous, ok := content["previous_content"].(string); ok {
			entry.PreviousContent = &previous
		}
		if err := a.Store.RecordMentalModelHistory(ctx, target, modelID, entry); err != nil {
			// A row whose model did not come back cannot be re-attached, and the
			// model's absence is the interesting fact, not the row's.
			return fmt.Errorf("write mental model history for %s: %w", modelID, err)
		}
		report.MentalModelHistory++
	}
	return nil
}

// applyOperationalHistory replays the audit trail and the model-request trace
// when the archive carries them and the restore asked for them.
func (a ImportApplier) applyOperationalHistory(ctx context.Context, req ImportRequest, target string, report *ImportReport) error {
	if !req.Scope.History || len(req.Archive.History) == 0 {
		return nil
	}
	for _, row := range req.Archive.History[hindsightarchive.AuditLogTable] {
		// The id is left to the target: these are globally unique, so a
		// same-instance restore would collide with the source's own row, and
		// nothing outside the log refers to the value.
		entry := storage.AuditEntry{
			Action:    rowString(row, "action"),
			Transport: rowString(row, "transport"),
			BankID:    target,
			StartedAt: rowTime(row, "started_at", a.now()),
			EndedAt:   rowTimePtr(row, "ended_at"),
			Request:   mapValue(row, "request"),
			Response:  mapValue(row, "response"),
			Metadata:  mapValue(row, "metadata"),
		}
		if err := a.Store.RecordAuditLog(ctx, entry); err != nil {
			return fmt.Errorf("write audit entry: %w", err)
		}
		report.HistoryRows++
	}
	for _, row := range req.Archive.History[hindsightarchive.LLMRequestsTable] {
		request := storage.LLMRequest{
			BankID:         target,
			Operation:      rowString(row, "operation"),
			Scope:          rowString(row, "scope"),
			TraceID:        rowString(row, "trace_id"),
			SpanID:         rowString(row, "span_id"),
			ParentSpanID:   rowString(row, "parent_span_id"),
			Provider:       rowString(row, "provider"),
			Model:          rowString(row, "model"),
			Status:         rowString(row, "status"),
			StartedAt:      rowTime(row, "started_at", a.now()),
			EndedAt:        rowTimePtr(row, "ended_at"),
			DurationMS:     rowIntPtr(row, "duration_ms"),
			InputTokens:    rowIntPtr(row, "input_tokens"),
			OutputTokens:   rowIntPtr(row, "output_tokens"),
			CachedTokens:   rowIntPtr(row, "cached_tokens"),
			ThoughtsTokens: rowIntPtr(row, "thoughts_tokens"),
			TotalTokens:    rowIntPtr(row, "total_tokens"),
			Input:          rowValue(row, "input"),
			Output:         rowValue(row, "output"),
			Error:          rowString(row, "error"),
			LLMInfo:        mapValue(row, "llm_info"),
			Metadata:       mapValue(row, "metadata"),
		}
		if err := a.Store.RecordLLMRequest(ctx, request); err != nil {
			return fmt.Errorf("write model request: %w", err)
		}
		report.HistoryRows++
	}
	return nil
}

// rowTimePtr reads an optional timestamp, reporting nil when it is absent.
func rowTimePtr(row map[string]any, key string) *time.Time {
	value := row[key]
	switch typed := value.(type) {
	case time.Time:
		return &typed
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, typed); err == nil {
			return &parsed
		}
	}
	return nil
}

// rowIntPtr reads an optional integer, reporting nil when it is absent.
func rowIntPtr(row map[string]any, key string) *int {
	if _, ok := row[key]; !ok || row[key] == nil {
		return nil
	}
	value := rowInt(row, key)
	return &value
}

// rowValue returns a field as it stands, for a column that carries free-form
// JSON.
func rowValue(row map[string]any, key string) any { return row[key] }

// mapString reads a string member of a decoded object.
func mapString(object map[string]any, key string) string {
	text, _ := object[key].(string)
	return text
}

// mapValue reads an object member of a decoded object, reporting nil when it is
// absent or not an object.
func mapValue(object map[string]any, key string) map[string]any {
	nested, _ := object[key].(map[string]any)
	return nested
}

// applyBankProfile writes the archive's bank row onto the target bank.
func (a ImportApplier) applyBankProfile(ctx context.Context, req ImportRequest, target string) error {
	if len(req.Archive.Banks) == 0 {
		return nil
	}
	row := req.Archive.Banks[0]
	name := rowString(row, "name")
	mission := rowString(row, "mission")
	disposition := rowIntMap(row, "disposition")
	config, _ := rowMap(row, "config")
	patch := storage.BankPatch{}
	if name != "" {
		patch.Name = &name
	}
	// The mission and the config are always applied: an archive that carries
	// neither is an archive of a bank that had none, and the target's own values
	// must not survive a restore.
	patch.Mission = &mission
	if config != nil {
		patch.Config = &config
	}
	if disposition != nil {
		patch.Disposition = &disposition
	}
	if _, err := a.Store.UpdateBank(ctx, target, patch); err != nil {
		return fmt.Errorf("restore the bank profile: %w", err)
	}
	return nil
}

// applyBankConfig restores the bank's own profile, then its directives and
// webhooks. The profile is what makes a clone a copy rather than an empty bank:
// the name, the graded traits, the mission and the configuration all come from
// the archive's bank row.
func (a ImportApplier) applyBankConfig(ctx context.Context, req ImportRequest, target string, report *ImportReport) error {
	if err := a.applyBankProfile(ctx, req, target); err != nil {
		return err
	}
	// A directive's and a webhook's id is unique across the whole schema rather
	// than within its bank, so a copy made on the same instance would collide
	// with the still-present original: the write would resolve onto the source
	// row, land in the wrong bank, and report the row as imported while the copy
	// owns none of it. Re-key both per target bank, the way knowledge-page nodes
	// are re-keyed, so the copy's rule set is its own. Nothing references either
	// id, so the rewrite is closed.
	for _, row := range req.Archive.Directives {
		sourceID := rowString(row, "id")
		if sourceID == "" {
			continue
		}
		id := stableArchiveID("directive", target, sourceID)
		directive := storage.Directive{
			ID:        id,
			BankID:    target,
			Name:      rowString(row, "name"),
			Content:   rowString(row, "content"),
			Priority:  rowInt(row, "priority"),
			IsActive:  rowBool(row, "is_active"),
			Tags:      nonNilStrings(rowStrings(row, "tags")),
			CreatedAt: rowTime(row, "created_at", a.now()),
			UpdatedAt: rowTime(row, "updated_at", a.now()),
		}
		if _, err := a.Store.PutDirective(ctx, directive); err != nil {
			return fmt.Errorf("write directive %s: %w", sourceID, err)
		}
		report.DirectivesImported++
	}
	for _, row := range req.Archive.Webhooks {
		sourceID := rowString(row, "id")
		if sourceID == "" {
			continue
		}
		id := stableArchiveID("webhook", target, sourceID)
		webhook := storage.Webhook{
			ID:         id,
			BankID:     target,
			URL:        rowString(row, "url"),
			Secret:     rowString(row, "secret"),
			EventTypes: nonNilStrings(rowStrings(row, "event_types")),
			Enabled:    rowBool(row, "enabled"),
			CreatedAt:  rowTime(row, "created_at", a.now()),
			UpdatedAt:  rowTime(row, "updated_at", a.now()),
		}
		if config, ok := rowMap(row, "http_config"); ok {
			webhook.HTTPConfig = config
		}
		if _, err := a.Store.PutWebhook(ctx, webhook); err != nil {
			return fmt.Errorf("write webhook %s: %w", sourceID, err)
		}
		report.WebhooksImported++
	}
	return nil
}

// embedUnits fills every unit's embedding in batches, so a large import does
// not send one unbounded request to the embedding provider. A deployment with
// no embedding model leaves every unit unembedded, exactly as a write does.
func (a ImportApplier) embedUnits(ctx context.Context, units []storage.MemoryUnit) error {
	if a.Embeddings == nil || len(units) == 0 {
		return nil
	}
	const batch = 64
	for start := 0; start < len(units); start += batch {
		end := start + batch
		if end > len(units) {
			end = len(units)
		}
		texts := make([]string, 0, end-start)
		for _, unit := range units[start:end] {
			texts = append(texts, truncateForEmbedding(unit.Text))
		}
		vectors, err := a.Embeddings.EmbedPassages(ctx, texts)
		if err != nil {
			return err
		}
		if len(vectors) != len(texts) {
			return fmt.Errorf("embedding generator returned %d vectors for %d texts", len(vectors), len(texts))
		}
		for i := range units[start:end] {
			if len(vectors[i]) > 0 {
				units[start+i].Embedding = vectors[i]
			}
		}
	}
	return nil
}

// now reports the applier's clock, defaulting to the wall clock.
func (a ImportApplier) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now().UTC()
}

// truncateForEmbedding mirrors the write path's input bound, so an imported
// fact and a retained one are embedded from the same text.
func truncateForEmbedding(text string) string {
	const max = 8192
	if len(text) <= max {
		return text
	}
	count := 0
	for index := range text {
		if count == max {
			return text[:index]
		}
		count++
	}
	return text
}

// archiveUnitID derives a deterministic unit identity for a fact. The archive
// carries no database id, so a stable per-(bank, document, ordinal) id keeps a
// re-import idempotent and lets an observation's evidence be remapped before
// the facts are written.
func archiveUnitID(bankID, documentID string, ordinal int) string {
	return stableArchiveID("unit", bankID, documentID, strconv.Itoa(ordinal))
}

// archiveDocumentID derives a fresh document identity for a conflict resolved
// as new-id, so an import that would collide with an existing document lands
// beside it under an id of its own.
func archiveDocumentID(bankID, sourceID string) string {
	return stableArchiveID("document", bankID, sourceID)
}

// archiveObservationID derives a deterministic identity for an imported
// observation.
func archiveObservationID(bankID string, index int, sourceID string) string {
	return stableArchiveID("observation", bankID, sourceID, strconv.Itoa(index))
}

// stableArchiveID builds a deterministic uuid identifier from its parts. The
// identity column is declared uuid, so the digest is laid out in the canonical
// 8-4-4-4-12 form with the version and variant bits set.
func stableArchiveID(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	buf := make([]byte, 16)
	copy(buf, sum[:16])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// observationKey addresses one fact within the archive's fact set.
func observationKey(documentID string, ordinal int) string {
	return documentID + "\x00" + strconv.Itoa(ordinal)
}

// proofCount keeps an observation's recorded evidence strength, falling back to
// the number of sources when the archive did not carry one.
func proofCount(recorded, sources int) int {
	if recorded > 0 {
		return recorded
	}
	if sources > 0 {
		return sources
	}
	return 1
}

// factObservationScopes renders a fact's stored scoping for the archive's list
// form. An unparseable or empty value yields no scopes, which is what an
// unscoped fact carries.
func factObservationScopes(fact hindsightarchive.Fact) json.RawMessage {
	if len(fact.ObservationScopes) == 0 {
		return nil
	}
	encoded, err := json.Marshal(fact.ObservationScopes)
	if err != nil {
		return nil
	}
	return encoded
}

// orderKnowledgePages sorts a tree's nodes so every parent precedes its
// children, which is the order the foreign key needs.
func orderKnowledgePages(pages []hindsightarchive.KnowledgePage) []hindsightarchive.KnowledgePage {
	byID := make(map[string]hindsightarchive.KnowledgePage, len(pages))
	for _, page := range pages {
		byID[page.ID] = page
	}
	ordered := make([]hindsightarchive.KnowledgePage, 0, len(pages))
	emitted := map[string]bool{}
	var emit func(page hindsightarchive.KnowledgePage)
	emit = func(page hindsightarchive.KnowledgePage) {
		if emitted[page.ID] {
			return
		}
		if page.ParentID != "" {
			if parent, ok := byID[page.ParentID]; ok {
				emit(parent)
			}
		}
		if emitted[page.ID] {
			return
		}
		emitted[page.ID] = true
		ordered = append(ordered, page)
	}
	for _, page := range pages {
		emit(page)
	}
	return ordered
}

// mentalModelFromRow rebuilds a mental model from an archive row, pinning it to
// the target bank so a copy never claims the source's identity.
func mentalModelFromRow(row map[string]any, bankID string, now time.Time) storage.MentalModel {
	model := storage.MentalModel{
		ID:                       rowString(row, "id"),
		BankID:                   bankID,
		Subtype:                  storage.MentalModelSubtype(rowString(row, "subtype")),
		Name:                     rowString(row, "name"),
		Description:              rowString(row, "description"),
		SourceQuery:              rowString(row, "source_query"),
		Content:                  rowString(row, "content"),
		Tags:                     nonNilStrings(rowStrings(row, "tags")),
		Links:                    nonNilStrings(rowStrings(row, "links")),
		MaxTokens:                rowInt(row, "max_tokens"),
		CreatedAt:                rowTime(row, "created_at", now),
		LastRefreshedAt:          rowTime(row, "last_refreshed_at", time.Time{}),
		LastRefreshedSourceQuery: rowString(row, "last_refreshed_source_query"),
	}
	if !storage.ValidMentalModelSubtype(model.Subtype) {
		model.Subtype = storage.SubtypePinned
	}
	if observations, ok := rowMap(row, "observations"); ok {
		model.Observations = observations
	}
	if trigger, ok := rowMap(row, "trigger"); ok {
		model.Trigger = trigger
	}
	if structured, ok := rowMap(row, "structured_content"); ok {
		model.Structured = structured
	}
	if reflect, ok := rowMap(row, "reflect_response"); ok {
		model.ReflectResult = reflect
	}
	return model
}

// rowString reads a string field from an archive row.
func rowString(row map[string]any, key string) string {
	value, ok := row[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

// rowInt reads an integer field from an archive row, accepting the numeric
// encodings JSON decoders produce.
func rowInt(row map[string]any, key string) int {
	value, ok := row[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}

// rowBool reads a boolean field from an archive row.
func rowBool(row map[string]any, key string) bool {
	value, ok := row[key]
	if !ok {
		return false
	}
	flag, _ := value.(bool)
	return flag
}

// rowStrings reads a string list from an archive row.
func rowStrings(row map[string]any, key string) []string {
	value, ok := row[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			out = append(out, text)
		}
		return out
	default:
		return nil
	}
}

// rowMap reads an object field from an archive row.
func rowMap(row map[string]any, key string) (map[string]any, bool) {
	value, ok := row[key]
	if !ok {
		return nil, false
	}
	object, ok := value.(map[string]any)
	return object, ok
}

// rowIntMap reads a graded-trait object from an archive row.
func rowIntMap(row map[string]any, key string) map[string]int {
	value, ok := row[key]
	if !ok {
		return nil
	}
	// An archive read from disk carries the object as map[string]any; one built
	// in this process still carries the map[string]int it was rendered from.
	if typed, ok := value.(map[string]int); ok {
		return typed
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]int, len(object))
	for name, raw := range object {
		switch typed := raw.(type) {
		case int:
			// An archive built in this process has not been through a JSON round
			// trip, so its traits are still ints rather than the float64 a
			// decoded document carries.
			out[name] = typed
		case float64:
			out[name] = int(typed)
		case json.Number:
			parsed, err := typed.Int64()
			if err != nil {
				continue
			}
			out[name] = int(parsed)
		}
	}
	return out
}

// rowTime reads a timestamp field from an archive row, returning the fallback
// when the archive did not carry one.
//
// A row decoded from the archive holds an RFC 3339 string, while one the
// builder produced in process still holds the time itself — the clone path
// hands the archive straight to the applier. Both shapes are accepted.
func rowTime(row map[string]any, key string, fallback time.Time) time.Time {
	switch value := row[key].(type) {
	case time.Time:
		return value.UTC()
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return fallback
		}
		return parsed.UTC()
	}
	return fallback
}
