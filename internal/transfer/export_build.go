package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transferarchive"
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
func BuildArchive(ctx context.Context, store storage.Store, bankID string, opts ExportOptions) (*transferarchive.Archive, error) {
	now := time.Now().UTC()
	if opts.Now != nil {
		now = opts.Now()
	}
	archive := &transferarchive.Archive{}
	archive.Manifest.SchemaVersion = transferarchive.SupportedSchemaVersion
	archive.Manifest.SourceBankID = bankID
	exportedAt := httpx.FormatZuluTimestamp(now)
	archive.Manifest.ExportedAt = &exportedAt

	documents, err := loadDocuments(ctx, store, bankID, opts.DocumentIDs)
	if err != nil {
		return nil, err
	}
	// unitLocations records where every exported fact sits, so an observation's
	// source references can be expressed as (document, ordinal) — the only form
	// that survives a transfer to a bank that mints fresh ids.
	unitLocations := map[string]transferarchive.ObservationSource{}
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

	// Two producers write an archive here, and they write different things.
	//
	// A whole-bank export (opts.Scope.Any()) is engine/transfer/export.py's
	// stream_export_bank: the documents, the observations, the attachment bytes,
	// the data tables, the synthesized knowledge and the bank config, each
	// selected by scope. A document export (a document subset, or the older
	// document-transfer route with no scope at all) is stream_export_documents:
	// documents only, plus observations and the knowledge base when asked for —
	// and its manifest declares `archive_type: "documents"` with `scope: null`.
	//
	// Deciding by `opts.Scope.Any()` rather than by "are there document ids"
	// matters: a whole-bank export with every scope flag off is refused by the
	// route before it reaches here, while a document export legitimately carries
	// no scope — and used to be written as a bank archive with an all-false
	// scope and a data section, which the reference's document export never has.
	if opts.Scope.Any() {
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
		graphQueue, err := buildGraphMaintenance(ctx, store, bankID)
		if err != nil {
			return nil, err
		}
		entityQueue, err := buildEntityMaintenance(ctx, store, bankID)
		if err != nil {
			return nil, err
		}
		// Every data table is carried, empty or not: the reference writes each
		// one on every whole-bank export, so a bank with no queued maintenance
		// still emits `data/graph_maintenance_queue.json` as `[]`.
		archive.Data = map[string][]map[string]any{
			transferarchive.AsyncOperationsTable:        operations,
			transferarchive.GraphMaintenanceQueueTable:  graphQueue,
			transferarchive.EntityMaintenanceQueueTable: entityQueue,
			transferarchive.InvalidatedMemoryTable:      archived,
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
		archive.Manifest.Scope = &transferarchive.Scope{
			Data:       opts.Scope.Data,
			BankConfig: opts.Scope.BankConfig,
			History:    opts.Scope.History,
		}
		archive.Manifest.ArchiveType = "bank"
		// A whole-bank archive's bank/history row files carry each jsonb column
		// as its JSON text, matching the reference's producer.
		encoding := transferarchive.EncodingSerialized
		archive.Manifest.BankRowsJSONEncoding = &encoding
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

// buildOperations reads the bank's operations log as rows.
//
// Every row travels, including the ones still in flight: the reference dumps
// the table with `SELECT *` (`engine/transfer/export.py:431-440`) and defers
// the choice to the receiving side, which drops the live ones on restore
// (`engine/transfer/importer.py:775-793`) because a restored `pending` row is
// work the target's worker would actually run. Filtering here instead would
// make the archive disagree with the reference's for the same bank, and would
// hide the fact from anyone comparing the two.
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
			out = append(out, operationRow(op))
		}
		if len(page.Operations) < pageSize {
			return out, nil
		}
	}
}

// operationRow renders one operation under the table's own column names, which
// is what the reference's `SELECT *` dump produces: an archived row is the row,
// not a projection of it, so an importer written against the reference reads
// the same keys.
//
// The two jsonb columns travel as JSON text, matching the archive's
// `bank_rows_json_encoding: "serialized"` -- the reference's asyncpg
// connection hands back a string for them and the dump keeps it.
func operationRow(op storage.Operation) map[string]any {
	row := map[string]any{
		"operation_id":      op.OperationID,
		"bank_id":           op.BankID,
		"operation_type":    op.OperationType,
		"status":            string(op.Status),
		"error_message":     errorOrNil(op.Error),
		"created_at":        timeOrNil(op.CreatedAt),
		"updated_at":        timeOrNil(op.UpdatedAt),
		"completed_at":      timeOrNil2(op.CompletedAt),
		"retry_count":       op.RetryCount,
		"next_retry_at":     timeOrNil2(op.NextRetryAt),
		"serialization_key": nullableString(op.SerializationKey),
		"worker_id":         nullableString(op.WorkerID),
		"claimed_at":        timeOrNil2(op.ClaimedAt),
	}
	if op.Payload != nil {
		row["task_payload"] = jsonText(op.Payload)
	} else {
		row["task_payload"] = nil
	}
	if op.ResultMetadata != nil {
		row["result_metadata"] = jsonText(op.ResultMetadata)
	} else {
		row["result_metadata"] = jsonText(nil)
	}
	return row
}

// nullableString renders an empty string as the NULL the column holds, which is
// how the reference's dump distinguishes "no value" from the empty string.
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// errorOrNil renders an empty error column as NULL rather than "".
func errorOrNil(value string) any {
	if value == "" {
		return nil
	}
	return value
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

// maintenanceQueueStore is the backend capability the transfer uses to carry the
// two maintenance work queues. A backend that does not implement it exports
// without them (empty segments) and imports none, which is what a store with no
// such queue has to offer.
type maintenanceQueueStore interface {
	ExportGraphMaintenance(ctx context.Context, bankID string) ([]storage.MaintenanceQueueRow, error)
	ImportGraphMaintenance(ctx context.Context, bankID string, rows []storage.MaintenanceQueueRow) (int, error)
	ExportEntityMaintenance(ctx context.Context, bankID string) ([]storage.MaintenanceQueueRow, error)
	ImportEntityMaintenance(ctx context.Context, bankID string, rows []storage.MaintenanceQueueRow) (int, error)
}

// buildGraphMaintenance reads the bank's queued graph-relink targets as archive
// rows, so the target can re-point each onto its own unit.
func buildGraphMaintenance(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, error) {
	queues, ok := store.(maintenanceQueueStore)
	if !ok {
		return nil, nil
	}
	rows, err := queues.ExportGraphMaintenance(ctx, bankID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"bank_id":     bankID,
			"unit_id":     row.ID,
			"enqueued_at": timeOrNil(row.EnqueuedAt),
		})
	}
	return out, nil
}

// buildEntityMaintenance reads the bank's queued entity-prune targets as archive
// rows. Each rides by its canonical name, because the target re-resolves the
// entity against its own rows rather than reusing the source's id.
func buildEntityMaintenance(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, error) {
	queues, ok := store.(maintenanceQueueStore)
	if !ok {
		return nil, nil
	}
	rows, err := queues.ExportEntityMaintenance(ctx, bankID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"bank_id":        bankID,
			"canonical_name": row.CanonicalName,
			"enqueued_at":    timeOrNil(row.EnqueuedAt),
		})
	}
	return out, nil
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
		history[transferarchive.AuditLogTable] = audit
	}
	if len(requests) > 0 {
		history[transferarchive.LLMRequestsTable] = requests
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
		"started_at": timeOrNil(entry.StartedAt),
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
		"started_at":      timeOrNil(request.StartedAt),
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

// timeOrNil2 renders a pointer time as the archive's row timestamp string, or
// nil when it was never set. The data and history sections are written by the
// reference through json.dumps(default=_row_json_default), whose datetime hook
// is Python's isoformat, so the string is the same form as every other HTTP
// response timestamp.
func timeOrNil2(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return httpx.FormatTimestamp(*value)
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
func buildAttachments(ctx context.Context, store storage.Store, bankID string, documents []transferarchive.Document) ([]transferarchive.Attachment, map[string][]byte, error) {
	var attachments []transferarchive.Attachment
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
			entry := transferarchive.BlobEntry(len(attachments))
			if blobs == nil {
				blobs = map[string][]byte{}
			}
			blobs[entry] = data
			attachments = append(attachments, transferarchive.Attachment{
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
func buildDocument(ctx context.Context, store storage.Store, bankID string, document storage.Document) (transferarchive.Document, map[string]transferarchive.ObservationSource, error) {
	built := transferarchive.Document{
		ID:           document.ID,
		OriginalText: document.OriginalText,
		RetainParams: archiveRetainParams(document.RetainParams),
		Tags:         document.Tags,
	}
	if !document.CreatedAt.IsZero() {
		created := httpx.FormatZuluTimestamp(document.CreatedAt)
		built.CreatedAt = &created
	}
	chunks, err := loadChunks(ctx, store, bankID, document.ID)
	if err != nil {
		return built, nil, err
	}
	chunkOrdinals := map[string]int{}
	for _, chunk := range chunks {
		built.Chunks = append(built.Chunks, transferarchive.Chunk{
			ChunkIndex: chunk.ChunkIndex,
			ChunkText:  chunk.ChunkText,
		})
		chunkOrdinals[chunk.ChunkID] = chunk.ChunkIndex
	}
	units, err := loadDocumentUnits(ctx, store, bankID, document.ID)
	if err != nil {
		return built, nil, err
	}
	locations := make(map[string]transferarchive.ObservationSource, len(units))
	for ordinal, unit := range units {
		fact := transferarchive.Fact{
			Text:                  unit.Text,
			SourceID:              unit.ID,
			FactType:              string(unit.FactType),
			Context:               unit.Context,
			EventDate:             httpx.FormatZuluTimestampPtr(unit.EventDate),
			OccurredStart:         httpx.FormatZuluTimestampPtr(unit.OccurredStart),
			OccurredEnd:           httpx.FormatZuluTimestampPtr(unit.OccurredEnd),
			MentionedAt:           httpx.FormatZuluTimestampPtr(unit.MentionedAt),
			Metadata:              mapMetadata(unit.Metadata),
			Tags:                  unit.Tags,
			ObservationScopes:     archiveScopeList(unit.ObservationScopes),
			CreatedAt:             httpx.FormatZuluTimestampPtr(unit.CreatedAt),
			ConsolidatedAt:        httpx.FormatZuluTimestampPtr(unit.ConsolidatedAt),
			ConsolidationFailedAt: httpx.FormatZuluTimestampPtr(unit.ConsolidationFailedAt),
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
		locations[unit.ID] = transferarchive.ObservationSource{DocumentID: document.ID, FactIndex: ordinal}
	}
	return built, locations, nil
}

// archiveRetainParams renders a stored document's retain parameters the way the
// reference's payload carries them.
//
// The column is `jsonb NOT NULL DEFAULT '{}'`, so the reference's `SELECT *`
// hands back a dict: an empty one for a document retained without parameters.
// This service keeps the same column, but reads an empty object back as a nil
// map, so a document with no parameters must still be written as `{}` — a
// `null` there is a value the reference's model refuses for the field, and its
// reader validates every document payload before replaying it.
func archiveRetainParams(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	return params
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
func buildObservations(ctx context.Context, store storage.Store, bankID string, locations map[string]transferarchive.ObservationSource) ([]transferarchive.Observation, error) {
	units, err := loadUnitsByType(ctx, store, bankID, storage.TypeObservation)
	if err != nil {
		return nil, err
	}
	var out []transferarchive.Observation
	for _, unit := range units {
		sources := make([]transferarchive.ObservationSource, 0, len(unit.SourceMemoryIDs))
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
		out = append(out, transferarchive.Observation{
			SourceID:          unit.ID,
			Text:              unit.Text,
			CreatedAt:         httpx.FormatZuluTimestampPtr(unit.CreatedAt),
			Tags:              unit.Tags,
			EventDate:         httpx.FormatZuluTimestampPtr(unit.EventDate),
			OccurredStart:     httpx.FormatZuluTimestampPtr(unit.OccurredStart),
			OccurredEnd:       httpx.FormatZuluTimestampPtr(unit.OccurredEnd),
			MentionedAt:       httpx.FormatZuluTimestampPtr(unit.MentionedAt),
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
func buildKnowledgeBase(ctx context.Context, store storage.Store, bankID string) ([]map[string]any, []transferarchive.KnowledgePage, error) {
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
	pages := make([]transferarchive.KnowledgePage, 0, len(nodes))
	for _, node := range nodes {
		page := transferarchive.KnowledgePage{
			ID:            node.ID,
			ParentID:      node.ParentID,
			Kind:          string(node.Kind),
			Name:          node.Title,
			MentalModelID: node.MentalModelID,
			SortOrder:     node.SortOrder,
			Managed:       node.Managed,
		}
		if !node.CreatedAt.IsZero() {
			created := httpx.FormatZuluTimestamp(node.CreatedAt)
			page.CreatedAt = &created
		}
		if !node.UpdatedAt.IsZero() {
			updated := httpx.FormatZuluTimestamp(node.UpdatedAt)
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
	row := bankRow(bank)
	// The reference dumps the bank row with every column, so the archive also
	// names the internal identity and the two lifecycle timestamps the Bank
	// projection does not hold.
	if source, ok := store.(storage.BankLifecycleSource); ok {
		life, err := source.BankLifecycle(ctx, bankID)
		if err != nil {
			return nil, nil, nil, err
		}
		if life.InternalID != "" {
			row["internal_id"] = life.InternalID
		}
		row["last_consolidated_at"] = timeOrNil2(life.LastConsolidatedAt)
		row["mission_changed_at"] = timeOrNil2(life.MissionChangedAt)
	}
	banks := []map[string]any{row}

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
//
// The reference dumps the table with `SELECT *` (engine/transfer/export.py:431),
// so the row is the whole table row minus the two derived columns it strips
// (`embedding`, `search_vector`): every declared column appears, explicitly
// null when the column holds nothing. An archive that named only the columns
// this service happens to project would drop the fields the reference's own
// columns carry — `last_refreshed_source_query`, `entity_id`,
// `last_memory_seen_at`, `last_refresh_failed_at` — and every missing key would
// read back as null on a restore.
func mentalModelRow(model storage.MentalModel) map[string]any {
	row := map[string]any{
		"id":                          model.ID,
		"bank_id":                     model.BankID,
		"name":                        model.Name,
		"source_query":                model.SourceQuery,
		"content":                     model.Content,
		"tags":                        nonNilStrings(model.Tags),
		"last_refreshed_at":           nullableTime(model.LastRefreshedAt),
		"created_at":                  nullableTime(model.CreatedAt),
		"reflect_response":            jsonTextPtr(model.ReflectResult),
		"max_tokens":                  model.MaxTokens,
		"trigger":                     jsonTextPtr(model.Trigger),
		"last_refreshed_source_query": nullableString(model.LastRefreshedSourceQuery),
		"structured_content":          jsonTextPtr(model.Structured),
		"subtype":                     string(model.Subtype),
		"description":                 model.Description,
		"entity_id":                   nullableString(model.EntityID),
		"observations":                jsonTextPtr(model.Observations),
		"links":                       nullableStrings(model.Links),
		"last_updated":                nullableTime(model.LastUpdated),
		"last_memory_seen_at":         nullableTimePtr(model.LastMemorySeenAt),
		"last_refresh_failed_at":      nullableTimePtr(model.LastRefreshFailedAt),
	}
	return row
}

// jsonTextPtr renders a jsonb column's value as its JSON text, or null when the
// column holds nothing. The row files travel as `bank_rows_json_encoding:
// "serialized"`, so every jsonb member is text rather than a decoded object.
func jsonTextPtr(value map[string]any) any {
	if value == nil {
		return nil
	}
	return jsonText(value)
}

// nullableTime renders a timestamp column, or null when it was never set.
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return timeOrNil(value)
}

// nullableTimePtr renders an optional timestamp column, or null when unset.
func nullableTimePtr(value *time.Time) any { return timeOrNil2(value) }

// nullableStrings renders a text[] column, or null when the column holds NULL.
// The reference distinguishes an empty array from NULL on this column (its
// default is no array at all), so the distinction is carried rather than
// collapsed.
func nullableStrings(values []string) any {
	if values == nil {
		return nil
	}
	return values
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
	// The two jsonb columns travel as their JSON text, which is how the
	// reference's producer writes every jsonb column of a bank/history row (its
	// manifest declares bank_rows_json_encoding: "serialized"). config is
	// always present, as an empty object when the bank overrides nothing.
	return map[string]any{
		"bank_id":     bank.BankID,
		"name":        bank.Name,
		"disposition": jsonText(dispositionJSON(bank.Disposition)),
		"mission":     bank.Mission,
		"created_at":  timeOrNil(bank.CreatedAt),
		"updated_at":  timeOrNil(bank.UpdatedAt),
		"config":      jsonText(bank.Config),
	}
}

// jsonText renders a jsonb column's value as the JSON text the reference's
// connection returns for it. An absent or unencodable value becomes an empty
// object, which is the column's not-null shape.
func jsonText(value any) string {
	if value == nil {
		return "{}"
	}
	// The reference's connection hands back a jsonb column as the text the
	// database stores it in, and PostgreSQL renders that text its own way (see
	// pgJSONText): keys ordered by length then bytewise, and a space after every
	// ':'. Go's encoder writes `{"a":1,"b":[1,2]}` in insertion order, so the
	// value is re-rendered here rather than passed through.
	return pgJSONText(value)
}

// pgJSONText renders a value the way PostgreSQL renders a jsonb column as text.
//
// jsonb does not preserve the order an object was written in: it stores the
// pairs sorted by key length and then bytewise (jsonb_object_ordering), and its
// text output puts `": "` and `", "` between members and items. Reproducing both
// here is what makes an archive's jsonb members compare equal to the
// reference's — the archive carries the column's TEXT, so the text is the value.
func pgJSONText(value any) string {
	var out bytes.Buffer
	writePGJSON(&out, value)
	return out.String()
}

// writePGJSON writes one value in PostgreSQL's jsonb text form.
func writePGJSON(out *bytes.Buffer, value any) {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		out.WriteString(typed.String())
	case float64:
		out.WriteString(strconv.FormatFloat(typed, 'g', -1, 64))
	case int:
		out.WriteString(strconv.Itoa(typed))
	case int64:
		out.WriteString(strconv.FormatInt(typed, 10))
	case string:
		writePGJSONString(out, typed)
	case []string:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
		writePGJSON(out, items)
	case []any:
		out.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				out.WriteString(", ")
			}
			writePGJSON(out, item)
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		out.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				out.WriteString(", ")
			}
			writePGJSONString(out, key)
			out.WriteString(": ")
			writePGJSON(out, typed[key])
		}
		out.WriteByte('}')
	default:
		// Anything else (a struct, a decoded numeric type) is rendered by the
		// standard encoder and then re-spaced, which keeps the function total
		// without inventing an order for a type jsonb never receives.
		encoded, err := json.Marshal(value)
		if err != nil {
			out.WriteString("null")
			return
		}
		out.WriteString(pyJSONSpacing(encoded))
	}
}

// writePGJSONString writes a JSON string with the escapes PostgreSQL uses: the
// two-character sequences, and control characters as \uXXXX in lower case hex.
func writePGJSONString(out *bytes.Buffer, value string) {
	out.WriteByte('"')
	for _, char := range value {
		switch char {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			if char < 0x20 {
				fmt.Fprintf(out, `\u%04x`, char)
				continue
			}
			out.WriteRune(char)
		}
	}
	out.WriteByte('"')
}

// pyJSONSpacing inserts the space PostgreSQL's jsonb output puts after every
// object-level ':' and removes the one a comma may carry. String literals are
// copied verbatim, so a ':' inside a value is never touched.
func pyJSONSpacing(encoded []byte) string {
	out := make([]byte, 0, len(encoded)+8)
	inString := false
	escaped := false
	for index := 0; index < len(encoded); index++ {
		ch := encoded[index]
		switch {
		case escaped:
			escaped = false
			out = append(out, ch)
		case inString && ch == '\\':
			escaped = true
			out = append(out, ch)
		case ch == '"':
			inString = !inString
			out = append(out, ch)
		case !inString && ch == ':':
			out = append(out, ':', ' ')
		case !inString && ch == ' ':
			// A space outside a string is layout the database never emits.
		default:
			out = append(out, ch)
		}
	}
	return string(out)
}

// dispositionJSON renders the bank's graded traits as the object the archive
// carries, in the general-purpose map shape jsonText knows how to render the way
// PostgreSQL would.
func dispositionJSON(disposition map[string]int) map[string]any {
	out := make(map[string]any, len(disposition))
	for key, value := range disposition {
		out[key] = value
	}
	return out
}

// archiveScopeList carries a stored observation scope onto the archive
// verbatim. The stored column and the archive's schema are the same union: a
// mode name string or an explicit list of tag groups, so the raw JSON is
// reused rather than narrowed onto one of the two shapes.
func archiveScopeList(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return raw
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

// timeOrNil renders a timestamp for a row, or nil when it was never set. See
// timeOrNil2 for why the form is Python's isoformat rather than a bare time.
func timeOrNil(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return httpx.FormatTimestamp(value)
}
