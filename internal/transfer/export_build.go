package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/storage"
)

// pageSize is how many rows one read requests when a section is streamed. The
// store is free to return fewer; the pager keeps asking until it sees a short
// page.
const pageSize = 200

// ExportScope selects which slices of a bank an archive carries. It mirrors the
// reference's three-boolean selector, so a caller never has to know the table
// layout.
type ExportScope struct {
	Data       bool
	BankConfig bool
	History    bool
}

// DefaultExportScope is a whole-bank export: the memories and the bank's
// configuration, without the operational history.
func DefaultExportScope() ExportScope { return ExportScope{Data: true, BankConfig: true} }

// Any reports whether any section is selected. A transfer must carry at least
// one, because an archive of nothing is never what a caller meant.
func (s ExportScope) Any() bool { return s.Data || s.BankConfig || s.History }

// ExportOptions controls one archive build.
type ExportOptions struct {
	// DocumentIDs narrows the export to a subset of documents. A subset is not
	// a bank, so no bank-level section is carried with it.
	DocumentIDs []string
	// IncludeObservations carries consolidated observations. Whole-bank only.
	IncludeObservations bool
	// IncludeKnowledgeBase carries mental models and the knowledge-page tree.
	// Whole-bank only.
	IncludeKnowledgeBase bool
	// Scope selects the whole-bank sections. It is ignored for a document
	// subset, which carries documents only.
	Scope ExportScope
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// BuildArchive reads a bank and assembles its transfer archive. The archive is
// the same format the reader understands, so an archive this builds is
// readable here, by the migration path, and by the reference's importer.
//
// Nothing here embeds a vector: an archive carries text and structure, and the
// importing instance regenerates embeddings with its own model, which is what
// makes an archive portable between instances configured differently.
func BuildArchive(ctx context.Context, store storage.Store, bankID string, opts ExportOptions) (*hindsightarchive.Archive, error) {
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now()
	}
	archive := &hindsightarchive.Archive{}
	archive.Manifest.SchemaVersion = hindsightarchive.SupportedSchemaVersion
	archive.Manifest.SourceBankID = bankID
	archive.Manifest.ExportedAt = &now

	documents, err := loadDocuments(ctx, store, bankID, opts.DocumentIDs)
	if err != nil {
		return nil, err
	}
	// unitLocations records where every exported fact sits, so an observation's
	// source references can be expressed as (document, ordinal) — the only form
	// that survives a transfer to a bank that mints fresh ids.
	unitLocations := map[string]hindsightarchive.ObservationSource{}
	for _, document := range documents {
		built, order, err := buildDocument(ctx, store, bankID, document)
		if err != nil {
			return nil, err
		}
		for unitID, source := range order {
			unitLocations[unitID] = source
		}
		archive.Documents = append(archive.Documents, built)
		archive.Manifest.FactCount += len(built.Facts)
	}
	archive.Manifest.DocumentCount = len(archive.Documents)

	// An attachment's bytes live beside its row, so a bank with attachments
	// cannot be exported without them: the rows travel with an entry naming the
	// bytes, and the importing instance recomputes the storage key.
	attachments, blobs, err := buildAttachments(ctx, store, bankID, archive.Documents)
	if err != nil {
		return nil, err
	}
	archive.Attachments = attachments
	archive.Blobs = blobs

	// A document subset carries no bank-level sections: observations can span
	// documents outside the subset, and the bank's configuration describes the
	// whole of it.
	if len(opts.DocumentIDs) == 0 {
		if opts.Scope.Data || opts.IncludeObservations || opts.IncludeKnowledgeBase {
			if opts.IncludeObservations {
				observations, err := buildObservations(ctx, store, bankID, unitLocations)
				if err != nil {
					return nil, err
				}
				archive.Observations = observations
			}
			if opts.IncludeKnowledgeBase {
				models, pages, err := buildKnowledgeBase(ctx, store, bankID)
				if err != nil {
					return nil, err
				}
				archive.MentalModels = models
				archive.KnowledgePages = pages
			}
		}
		// The operations log and the curation archive ride with the data
		// section: the first records what the bank did, the second holds units
		// the caller invalidated and can still revert.
		operations, err := buildOperations(ctx, store, bankID)
		if err != nil {
			return nil, err
		}
		archived, err := buildInvalidatedMemories(ctx, store, bankID)
		if err != nil {
			return nil, err
		}
		if len(operations) > 0 || len(archived) > 0 {
			archive.Data = map[string][]map[string]any{}
			if len(operations) > 0 {
				archive.Data[hindsightarchive.AsyncOperationsTable] = operations
			}
			if len(archived) > 0 {
				archive.Data[hindsightarchive.InvalidatedMemoryTable] = archived
			}
		}
		archive.Manifest.OperationCount = len(operations)
		archive.Manifest.InvalidatedMemoryCount = len(archived)
		// A model's refresh log rides with the model, and the operational trail
		// only when the history section was selected.
		history, err := buildMentalModelHistory(ctx, store, bankID, archive.MentalModels)
		if err != nil {
			return nil, err
		}
		archive.MentalModelHistory = history
		if opts.Scope.History {
			carried, err := buildOperationalHistory(ctx, store, bankID)
			if err != nil {
				return nil, err
			}
			archive.History = carried
		}
		if opts.Scope.BankConfig {
			bankRows, directives, webhooks, err := buildBankConfig(ctx, store, bankID)
			if err != nil {
				return nil, err
			}
			archive.Banks = bankRows
			archive.Directives = directives
			archive.Webhooks = webhooks
		}
		archive.Manifest.Scope = &hindsightarchive.Scope{
			Data:       opts.Scope.Data,
			BankConfig: opts.Scope.BankConfig,
			History:    opts.Scope.History,
		}
		archive.Manifest.ArchiveType = "bank"
	} else {
		archive.Manifest.ArchiveType = "documents"
	}
	archive.Manifest.ObservationCount = len(archive.Observations)
	archive.Manifest.MentalModelCount = len(archive.MentalModels)
	archive.Manifest.KnowledgePageCount = len(archive.KnowledgePages)
	archive.Manifest.DirectiveCount = len(archive.Directives)
	archive.Manifest.WebhookCount = len(archive.Webhooks)
	archive.Manifest.IncludesHistory = opts.Scope.History
	archive.Manifest.AttachmentCount = len(archive.Attachments)
	return archive, nil
}

// buildOperations reads the bank's finished operations as rows.
//
// Only finished ones travel. An operation in flight names work the source
// instance still owes, and a target that restored it would run work that was
// never its own — the reference drops them for the same reason.
func buildOperations(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, error) {
	var out []map[string]any
	for offset := 0; ; offset += pageSize {
		page, err := store.ListOperations(ctx, storage.OperationQuery{
			BankID: bankID, Limit: pageSize, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		for _, op := range page.Operations {
			if !op.Status.Terminal() {
				continue
			}
			out = append(out, operationRow(op))
		}
		if len(page.Operations) < pageSize {
			return out, nil
		}
	}
}

// operationRow renders one operation under the table's column names. The
// identifier is not carried: the target's own log is about the target, and an
// id the source minted would only be re-used by accident.
func operationRow(op storage.Operation) map[string]any {
	row := map[string]any{
		"bank_id":           op.BankID,
		"operation_type":    op.OperationType,
		"status":            string(op.Status),
		"error":             op.Error,
		"created_at":        op.CreatedAt,
		"updated_at":        op.UpdatedAt,
		"completed_at":      timeOrNil2(op.CompletedAt),
		"retry_count":       op.RetryCount,
		"next_retry_at":     timeOrNil2(op.NextRetryAt),
		"serialization_key": op.SerializationKey,
		"is_parent":         op.IsParent,
	}
	if len(op.Payload) > 0 {
		row["payload"] = op.Payload
	}
	if len(op.ResultMetadata) > 0 {
		row["result_metadata"] = op.ResultMetadata
	}
	return row
}

// buildInvalidatedMemories reads the bank's curation archive, when the backend
// keeps one. A backend without it exports without the archive, which is what
// the reference does for a store that cannot list it.
func buildInvalidatedMemories(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, error) {
	archived, ok := store.(storage.ArchivedMemoryStore)
	if !ok {
		return nil, nil
	}
	return archived.ListArchivedMemories(ctx, bankID)
}

// buildMentalModelHistory reads the refresh log of every mental model the
// archive carries.
//
// The surrogate row id is dropped, because the target reassigns it from its own
// identity sequence — carrying explicit ids would leave that sequence
// unadvanced and collide with a later refresh. Rows go oldest-first so the
// reassigned ids keep the chronological tie-break the read path relies on.
func buildMentalModelHistory(ctx context.Context, store storage.Store, bankID string, models []map[string]any) ([]map[string]any, error) {
	var out []map[string]any
	for _, model := range models {
		modelID := rowString(model, "id")
		if modelID == "" {
			continue
		}
		entries, err := store.MentalModelHistory(ctx, bankID, modelID)
		if err != nil {
			return nil, err
		}
		// The read is newest-first; the archive is oldest-first.
		for index := len(entries) - 1; index >= 0; index-- {
			entry := entries[index]
			out = append(out, map[string]any{
				"mental_model_id": modelID,
				"bank_id":         bankID,
				"content":         entry.Payload(),
				"changed_at":      entry.ChangedAt,
			})
		}
	}
	return out, nil
}

// buildOperationalHistory reads the tables the history section selects: the
// audit trail and the model-request trace, as verbatim rows.
func buildOperationalHistory(ctx context.Context, store storage.Store, bankID string) (map[string][]map[string]any, error) {
	audit, err := loadAuditRows(ctx, store, bankID)
	if err != nil {
		return nil, err
	}
	requests, err := loadLLMRequestRows(ctx, store, bankID)
	if err != nil {
		return nil, err
	}
	history := map[string][]map[string]any{}
	if len(audit) > 0 {
		history[hindsightarchive.AuditLogTable] = audit
	}
	if len(requests) > 0 {
		history[hindsightarchive.LLMRequestsTable] = requests
	}
	if len(history) == 0 {
		return nil, nil
	}
	return history, nil
}

// loadAuditRows pages the bank's audit trail into archive rows, oldest first so
// a restore replays it in the order it happened.
func loadAuditRows(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, error) {
	var out []map[string]any
	for offset := 0; ; offset += pageSize {
		page, err := store.ListAuditLog(ctx, storage.AuditQuery{BankID: bankID, Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Entries {
			out = append(out, auditRow(entry))
		}
		if len(page.Entries) < pageSize {
			return out, nil
		}
	}
}

// auditRow renders one audit entry under the table's column names.
func auditRow(entry storage.AuditEntry) map[string]any {
	return map[string]any{
		"id":         entry.ID,
		"action":     entry.Action,
		"transport":  entry.Transport,
		"bank_id":    entry.BankID,
		"started_at": entry.StartedAt,
		"ended_at":   timeOrNil2(entry.EndedAt),
		"request":    entry.Request,
		"response":   entry.Response,
		"metadata":   entry.Metadata,
	}
}

// loadLLMRequestRows pages the bank's model-request trace into archive rows.
func loadLLMRequestRows(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, error) {
	var out []map[string]any
	for offset := 0; ; offset += pageSize {
		page, err := store.ListLLMRequests(ctx, storage.LLMRequestQuery{BankID: bankID, Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, request := range page.Requests {
			out = append(out, llmRequestRow(request))
		}
		if len(page.Requests) < pageSize {
			return out, nil
		}
	}
}

// llmRequestRow renders one model-request trace under the table's column names.
func llmRequestRow(request storage.LLMRequest) map[string]any {
	return map[string]any{
		"id":              request.ID,
		"bank_id":         request.BankID,
		"operation":       request.Operation,
		"scope":           request.Scope,
		"trace_id":        request.TraceID,
		"span_id":         request.SpanID,
		"parent_span_id":  request.ParentSpanID,
		"provider":        request.Provider,
		"model":           request.Model,
		"status":          request.Status,
		"started_at":      request.StartedAt,
		"ended_at":        timeOrNil2(request.EndedAt),
		"duration_ms":     intOrNil(request.DurationMS),
		"input_tokens":    intOrNil(request.InputTokens),
		"output_tokens":   intOrNil(request.OutputTokens),
		"cached_tokens":   intOrNil(request.CachedTokens),
		"thoughts_tokens": intOrNil(request.ThoughtsTokens),
		"total_tokens":    intOrNil(request.TotalTokens),
		"input":           request.Input,
		"output":          request.Output,
		"error":           request.Error,
		"llm_info":        request.LLMInfo,
		"metadata":        request.Metadata,
	}
}

// timeOrNil2 renders a pointer time as itself or nil, so an absent value is a
// JSON null rather than a zero timestamp.
func timeOrNil2(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

// intOrNil renders a pointer integer as itself or nil.
func intOrNil(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

// buildAttachments reads every exported document's attachment rows and their
// bytes. The entry names are ordinals rather than storage keys, because a key
// is caller-shaped and must never become a path inside the archive.
//
// An attachment whose bytes are absent from storage is skipped rather than
// carried as an empty blob: the row would describe bytes the archive does not
// hold, and an importer cannot tell that from a genuinely empty attachment.
func buildAttachments(ctx context.Context, store storage.Store, bankID string, documents []hindsightarchive.Document) ([]hindsightarchive.Attachment, map[string][]byte, error) {
	var attachments []hindsightarchive.Attachment
	var blobs map[string][]byte
	for _, document := range documents {
		rows, err := store.ListAttachments(ctx, bankID, document.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			data, ok, err := attachmentBytes(ctx, store, row)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				continue
			}
			entry := hindsightarchive.BlobEntry(len(attachments))
			if blobs == nil {
				blobs = map[string][]byte{}
			}
			blobs[entry] = data
			attachments = append(attachments, hindsightarchive.Attachment{
				BankID:         bankID,
				DocumentID:     document.ID,
				AttachmentHash: row.AttachmentHash,
				ShortID:        row.ShortID,
				MediaType:      row.MediaType,
				ByteSize:       int64(len(data)),
				Kind:           row.Kind,
				Filename:       row.Filename,
				Entry:          entry,
			})
		}
	}
	return attachments, blobs, nil
}

// attachmentBytes reads one attachment's payload, reporting false when the row
// names bytes storage does not hold.
func attachmentBytes(ctx context.Context, store storage.Store, row storage.Attachment) ([]byte, bool, error) {
	file, err := store.GetStoredFile(ctx, row.StorageKey)
	if err != nil {
		if errors.Is(err, storage.ErrStoredFileNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return file.Data, true, nil
}

// loadDocuments reads the bank's documents, or the named subset.
func loadDocuments(ctx context.Context, store storage.Store, bankID string, ids []string) ([]storage.Document, error) {
	if len(ids) > 0 {
		documents := make([]storage.Document, 0, len(ids))
		for _, id := range ids {
			document, err := store.GetDocument(ctx, bankID, id)
			if err != nil {
				return nil, fmt.Errorf("read document %s: %w", id, err)
			}
			documents = append(documents, document)
		}
		return documents, nil
	}
	var out []storage.Document
	offset := 0
	for {
		page, err := store.ListDocuments(ctx, storage.DocumentQuery{BankID: bankID, Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Documents...)
		if len(page.Documents) < pageSize {
			return out, nil
		}
		offset += len(page.Documents)
	}
}

// buildDocument turns one stored document into its archive payload and reports
// where each of its facts travelled.
func buildDocument(ctx context.Context, store storage.Store, bankID string, document storage.Document) (hindsightarchive.Document, map[string]hindsightarchive.ObservationSource, error) {
	built := hindsightarchive.Document{
		ID:           document.ID,
		OriginalText: document.OriginalText,
		RetainParams: document.RetainParams,
		Tags:         document.Tags,
	}
	if !document.CreatedAt.IsZero() {
		created := document.CreatedAt.UTC()
		built.CreatedAt = &created
	}
	chunks, err := loadChunks(ctx, store, bankID, document.ID)
	if err != nil {
		return built, nil, err
	}
	chunkOrdinals := map[string]int{}
	for _, chunk := range chunks {
		built.Chunks = append(built.Chunks, hindsightarchive.Chunk{
			ChunkIndex: chunk.ChunkIndex,
			ChunkText:  chunk.ChunkText,
		})
		chunkOrdinals[chunk.ChunkID] = chunk.ChunkIndex
	}
	units, err := loadDocumentUnits(ctx, store, bankID, document.ID)
	if err != nil {
		return built, nil, err
	}
	locations := make(map[string]hindsightarchive.ObservationSource, len(units))
	for ordinal, unit := range units {
		fact := hindsightarchive.Fact{
			Text:                  unit.Text,
			SourceID:              unit.ID,
			FactType:              string(unit.FactType),
			Context:               unit.Context,
			EventDate:             unit.EventDate,
			OccurredStart:         unit.OccurredStart,
			OccurredEnd:           unit.OccurredEnd,
			MentionedAt:           unit.MentionedAt,
			Metadata:              mapMetadata(unit.Metadata),
			Tags:                  unit.Tags,
			ObservationScopes:     archiveScopeList(unit.ObservationScopes),
			CreatedAt:             unit.CreatedAt,
			ConsolidatedAt:        unit.ConsolidatedAt,
			ConsolidationFailedAt: unit.ConsolidationFailedAt,
		}
		if unit.ChunkID != "" {
			if index, ok := chunkOrdinals[unit.ChunkID]; ok {
				fact.ChunkIndex = &index
			}
		}
		entities, err := store.UnitEntities(ctx, bankID, unit.ID)
		if err != nil {
			return built, nil, err
		}
		fact.Entities = entities
		built.Facts = append(built.Facts, fact)
		locations[unit.ID] = hindsightarchive.ObservationSource{DocumentID: document.ID, FactIndex: ordinal}
	}
	return built, locations, nil
}

// loadChunks reads every chunk of a document in index order.
func loadChunks(ctx context.Context, store storage.Store, bankID, documentID string) ([]storage.Chunk, error) {
	var out []storage.Chunk
	offset := 0
	for {
		page, err := store.ListChunks(ctx, storage.ChunkQuery{
			BankID: bankID, DocumentID: documentID, Limit: pageSize, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Chunks...)
		if len(page.Chunks) < pageSize {
			return out, nil
		}
		offset += len(page.Chunks)
	}
}

// loadDocumentUnits reads a document's facts in a stable order. Observations
// are excluded: they are bank-level, and the reference carries them in their
// own section.
func loadDocumentUnits(ctx context.Context, store storage.Store, bankID, documentID string) ([]storage.MemoryUnit, error) {
	var out []storage.MemoryUnit
	offset := 0
	for {
		page, err := store.ListMemories(ctx, storage.MemoryQuery{
			BankID: bankID, DocumentID: documentID, Limit: pageSize, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		for _, unit := range page.Memories {
			if unit.FactType == storage.TypeObservation {
				continue
			}
			out = append(out, unit)
		}
		if len(page.Memories) < pageSize {
			return out, nil
		}
		offset += len(page.Memories)
	}
}

// buildObservations reads the bank's observations. An observation whose source
// facts are not all in the archive is dropped rather than carried with
// dangling evidence, because the importing bank would resolve them to nothing.
func buildObservations(ctx context.Context, store storage.Store, bankID string, locations map[string]hindsightarchive.ObservationSource) ([]hindsightarchive.Observation, error) {
	units, err := loadUnitsByType(ctx, store, bankID, storage.TypeObservation)
	if err != nil {
		return nil, err
	}
	var out []hindsightarchive.Observation
	for _, unit := range units {
		sources := make([]hindsightarchive.ObservationSource, 0, len(unit.SourceMemoryIDs))
		for _, sourceID := range unit.SourceMemoryIDs {
			location, ok := locations[sourceID]
			if !ok {
				continue
			}
			sources = append(sources, location)
		}
		if len(sources) == 0 {
			continue
		}
		out = append(out, hindsightarchive.Observation{
			SourceID:          unit.ID,
			Text:              unit.Text,
			CreatedAt:         unit.CreatedAt,
			Tags:              unit.Tags,
			EventDate:         unit.EventDate,
			OccurredStart:     unit.OccurredStart,
			OccurredEnd:       unit.OccurredEnd,
			MentionedAt:       unit.MentionedAt,
			ObservationScopes: unit.ObservationScopes,
			ProofCount:        unit.ProofCount,
			Sources:           sources,
		})
	}
	return out, nil
}

// loadUnitsByType reads every unit of one fact type.
func loadUnitsByType(ctx context.Context, store storage.Store, bankID string, factType storage.MemoryUnitType) ([]storage.MemoryUnit, error) {
	var out []storage.MemoryUnit
	offset := 0
	for {
		page, err := store.ListMemories(ctx, storage.MemoryQuery{
			BankID: bankID, Type: string(factType), Limit: pageSize, Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Memories...)
		if len(page.Memories) < pageSize {
			return out, nil
		}
		offset += len(page.Memories)
	}
}

// buildKnowledgeBase reads the mental models and the knowledge-page tree. The
// pages travel as typed rows so the hierarchy, the managed flags and the
// ordering survive; their backing models travel alongside them.
func buildKnowledgeBase(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, []hindsightarchive.KnowledgePage, error) {
	models, err := store.ListMentalModels(ctx, bankID, "")
	if err != nil {
		return nil, nil, err
	}
	rows := make([]map[string]any, 0, len(models))
	for _, model := range models {
		rows = append(rows, mentalModelRow(model))
	}
	nodes, err := store.ListKnowledgeNodes(ctx, bankID)
	if err != nil {
		return nil, nil, err
	}
	pages := make([]hindsightarchive.KnowledgePage, 0, len(nodes))
	for _, node := range nodes {
		page := hindsightarchive.KnowledgePage{
			ID:            node.ID,
			ParentID:      node.ParentID,
			Kind:          string(node.Kind),
			Name:          node.Title,
			MentalModelID: node.MentalModelID,
			SortOrder:     node.SortOrder,
			Managed:       node.Managed,
		}
		if !node.CreatedAt.IsZero() {
			created := node.CreatedAt.UTC()
			page.CreatedAt = &created
		}
		if !node.UpdatedAt.IsZero() {
			updated := node.UpdatedAt.UTC()
			page.UpdatedAt = &updated
		}
		pages = append(pages, page)
	}
	return rows, pages, nil
}

// buildBankConfig reads the bank's own row, its directives and its webhooks.
func buildBankConfig(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, []map[string]any, []map[string]any, error) {
	bank, err := store.GetBank(ctx, bankID)
	if err != nil {
		return nil, nil, nil, err
	}
	banks := []map[string]any{bankRow(bank)}

	directives, err := store.ListDirectives(ctx, bankID)
	if err != nil {
		return nil, nil, nil, err
	}
	directiveRows := make([]map[string]any, 0, len(directives))
	for _, directive := range directives {
		directiveRows = append(directiveRows, directiveRow(directive))
	}
	webhooks, err := store.ListWebhooks(ctx, bankID)
	if err != nil {
		return nil, nil, nil, err
	}
	webhookRows := make([]map[string]any, 0, len(webhooks))
	for _, webhook := range webhooks {
		webhookRows = append(webhookRows, webhookRow(webhook))
	}
	return banks, directiveRows, webhookRows, nil
}

// mentalModelRow renders a mental model as the row shape the archive carries.
// The field names mirror the stored column names, so an archive produced here
// and one produced by the reference describe the same model the same way.
func mentalModelRow(model storage.MentalModel) map[string]any {
	row := map[string]any{
		"id":                model.ID,
		"bank_id":           model.BankID,
		"subtype":           string(model.Subtype),
		"name":              model.Name,
		"description":       model.Description,
		"source_query":      model.SourceQuery,
		"content":           model.Content,
		"tags":              nonNilStrings(model.Tags),
		"max_tokens":        model.MaxTokens,
		"links":             nonNilStrings(model.Links),
		"last_refreshed_at": timeOrNil(model.LastRefreshedAt),
		"last_updated":      timeOrNil(model.LastUpdated),
		"created_at":        timeOrNil(model.CreatedAt),
	}
	if model.Observations != nil {
		row["observations"] = model.Observations
	}
	if model.Trigger != nil {
		row["trigger"] = model.Trigger
	}
	if model.Structured != nil {
		row["structured_content"] = model.Structured
	}
	if model.ReflectResult != nil {
		row["reflect_response"] = model.ReflectResult
	}
	return row
}

// directiveRow renders a directive as the archive's row shape.
func directiveRow(directive storage.Directive) map[string]any {
	return map[string]any{
		"id":         directive.ID,
		"bank_id":    directive.BankID,
		"name":       directive.Name,
		"content":    directive.Content,
		"priority":   directive.Priority,
		"is_active":  directive.IsActive,
		"tags":       nonNilStrings(directive.Tags),
		"created_at": timeOrNil(directive.CreatedAt),
		"updated_at": timeOrNil(directive.UpdatedAt),
	}
}

// webhookRow renders a webhook as the archive's row shape. The secret travels
// with the registration, because a copy whose deliveries are unsigned is not
// the same registration.
func webhookRow(webhook storage.Webhook) map[string]any {
	row := map[string]any{
		"id":          webhook.ID,
		"bank_id":     webhook.BankID,
		"url":         webhook.URL,
		"secret":      webhook.Secret,
		"event_types": nonNilStrings(webhook.EventTypes),
		"enabled":     webhook.Enabled,
		"created_at":  timeOrNil(webhook.CreatedAt),
		"updated_at":  timeOrNil(webhook.UpdatedAt),
	}
	if webhook.HTTPConfig != nil {
		row["http_config"] = webhook.HTTPConfig
	}
	return row
}

// bankRow renders a bank as the archive's row shape. Aliases are deliberately
// absent: they are routing identity, and carrying them would make the copy
// claim the source's ids.
func bankRow(bank storage.Bank) map[string]any {
	row := map[string]any{
		"bank_id":     bank.BankID,
		"name":        bank.Name,
		"disposition": dispositionJSON(bank.Disposition),
		"mission":     bank.Mission,
		"created_at":  timeOrNil(bank.CreatedAt),
		"updated_at":  timeOrNil(bank.UpdatedAt),
	}
	if len(bank.Config) > 0 {
		row["config"] = bank.Config
	}
	return row
}

// dispositionJSON renders the bank's graded traits as the object the archive
// carries.
func dispositionJSON(disposition map[string]int) map[string]int {
	if disposition == nil {
		return map[string]int{}
	}
	return disposition
}

// archiveScopeList narrows a stored observation scope onto the archive's list
// form. The stored column accepts a mode name as well as an explicit group
// list, and only the explicit list is carried, because the archive's schema is
// a list.
func archiveScopeList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var explicit []string
	if err := json.Unmarshal(raw, &explicit); err == nil {
		return explicit
	}
	return nil
}

// mapMetadata copies a unit's metadata into a plain map for encoding. The list
// is empty rather than absent, so a reader never has to tell the two apart.
func mapMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	return metadata
}

// nonNilStrings returns a slice that is empty rather than nil, so a JSON
// payload carries [] instead of null.
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// timeOrNil renders a timestamp for a row, or nil when it was never set.
func timeOrNil(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
