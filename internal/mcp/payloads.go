package mcp

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// This file builds the payloads the reference's MCP tools return. The reference
// answers a tool call with whatever its engine returned, which is not always the
// shape the HTTP route serves: the memory and document listings are the same
// rows, but the operation read, the bank statistics and the mental-model
// projections are the engine's own dicts. Each builder below names the
// projection it reproduces so a later change to either side is deliberate.

// ---------------------------------------------------------------------------
// Bank probes and shared error answers

// bankMissing reports whether the bank is absent. The reference's list tools
// call into a read path that first requires the bank to exist, which is a
// normal negative answer rather than a transport failure.
func (s *Server) bankMissing(ctx context.Context, bankID string) bool {
	_, err := s.store.GetBank(ctx, bankID)
	return errors.Is(err, storage.ErrBankNotFound)
}

// bankMissingAnswer is the answer those tools give for an unknown bank. The
// sentence is the engine's own, which the reference surfaces verbatim.
func bankMissingAnswer(bankID string, extras ...jsonField) toolText {
	return errorPayload("Operation validation failed: Bank '"+bankID+"' not found", extras...)
}

// notFoundAnswer renders the reference's expected-failure answer, which is a
// normal result carrying an error sentence rather than an error result.
func notFoundAnswer(message string) toolText {
	return errorPayload(message)
}

// ---------------------------------------------------------------------------
// Memory units

// memoryListItem is one memory unit as the reference's MCP listing renders it,
// the same row the HTTP listing serves.
type memoryListItem struct {
	ID                    string           `json:"id"`
	Text                  string           `json:"text"`
	Context               string           `json:"context"`
	Date                  string           `json:"date"`
	FactType              *string          `json:"fact_type"`
	DocumentID            *string          `json:"document_id"`
	MentionedAt           *string          `json:"mentioned_at"`
	OccurredStart         *string          `json:"occurred_start"`
	OccurredEnd           *string          `json:"occurred_end"`
	Entities              string           `json:"entities"`
	ChunkID               *string          `json:"chunk_id"`
	ProofCount            int              `json:"proof_count"`
	Tags                  []string         `json:"tags"`
	Metadata              map[string]any   `json:"metadata"`
	ConsolidatedAt        *string          `json:"consolidated_at"`
	ConsolidationFailedAt *string          `json:"consolidation_failed_at"`
	State                 string           `json:"state"`
	InvalidationReason    *string          `json:"invalidation_reason"`
	InvalidatedAt         *string          `json:"invalidated_at"`
	EditedAt              *string          `json:"edited_at"`
	UpdatedAt             *string          `json:"updated_at"`
	SourceMemoryIDs       []string         `json:"source_memory_ids"`
	Attachments           []map[string]any `json:"attachments,omitempty"`
}

// memoryListPage is the listing envelope, whose members keep the reference's
// order.
type memoryListPage struct {
	Items  []memoryListItem `json:"items"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// memoryListItems renders a page of memory units, resolving the page's entity
// postings and attachments in one lookup each. The listing shows a unit's own
// postings, the projection the HTTP listing applies.
func (s *Server) memoryListItems(ctx context.Context, bankID string, units []storage.MemoryUnit) ([]memoryListItem, error) {
	unitIDs := make([]string, 0, len(units))
	for _, unit := range units {
		unitIDs = append(unitIDs, unit.ID)
	}
	var entityNames map[string][]string
	var err error
	if direct, ok := s.store.(directEntityStore); ok {
		entityNames, err = direct.DirectUnitEntityNames(ctx, bankID, unitIDs)
	} else {
		entityNames, err = s.store.UnitEntityNames(ctx, bankID, unitIDs)
	}
	if err != nil {
		return nil, err
	}
	items := make([]memoryListItem, 0, len(units))
	for _, unit := range units {
		item := toMemoryListItem(unit)
		if names, ok := entityNames[unit.ID]; ok {
			item.Entities = strings.Join(names, ", ")
		}
		items = append(items, item)
	}
	if attachments, ok := s.store.(storage.MemoryAttachmentStore); ok {
		byUnit, err := attachments.MemoryAttachments(ctx, bankID, unitIDs)
		if err != nil {
			return nil, err
		}
		for index := range items {
			if records := byUnit[items[index].ID]; len(records) > 0 {
				items[index].Attachments = memoryAttachmentItems(bankID, records)
			}
		}
	}
	return items, nil
}

// directEntityStore is implemented by a store that reports a unit's own entity
// postings without the observation → source inheritance the detail read applies.
type directEntityStore interface {
	DirectUnitEntityNames(ctx context.Context, bankID string, unitIDs []string) (map[string][]string, error)
}

// toMemoryListItem renders one stored unit as the listing serves it.
func toMemoryListItem(unit storage.MemoryUnit) memoryListItem {
	tags := unit.Tags
	if tags == nil {
		tags = []string{}
	}
	sources := unit.SourceMemoryIDs
	if sources == nil {
		sources = []string{}
	}
	metadata := unit.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	date := ""
	if unit.EventDate != nil {
		date = httpx.FormatTimestamp(*unit.EventDate)
	}
	var factType *string
	if unit.FactType != "" {
		value := string(unit.FactType)
		factType = &value
	}
	return memoryListItem{
		ID: unit.ID, Text: unit.Text, Context: unit.Context, Date: date,
		FactType: factType, DocumentID: optionalString(unit.DocumentID),
		ChunkID: optionalString(unit.ChunkID), Tags: tags, Metadata: metadata,
		OccurredStart:         httpx.FormatTimestampPtr(unit.OccurredStart),
		OccurredEnd:           httpx.FormatTimestampPtr(unit.OccurredEnd),
		MentionedAt:           httpx.FormatTimestampPtr(unit.MentionedAt),
		ProofCount:            unit.ProofCount,
		ConsolidatedAt:        httpx.FormatTimestampPtr(unit.ConsolidatedAt),
		ConsolidationFailedAt: httpx.FormatTimestampPtr(unit.ConsolidationFailedAt),
		State:                 memoryState(unit),
		InvalidationReason:    unit.InvalidationReason,
		InvalidatedAt:         httpx.FormatTimestampPtr(unit.InvalidatedAt),
		UpdatedAt:             httpx.FormatTimestampPtr(unit.UpdatedAt),
		EditedAt:              httpx.FormatTimestampPtr(unit.EditedAt),
		SourceMemoryIDs:       sources,
	}
}

// memoryDetail is one memory unit's detail read. The members are ordered as the
// reference emits them; the members of an observation's projection follow.
type memoryDetail struct {
	ID                 string           `json:"id"`
	Text               string           `json:"text"`
	Context            string           `json:"context"`
	Date               string           `json:"date"`
	Type               any              `json:"type"`
	MentionedAt        *string          `json:"mentioned_at"`
	OccurredStart      *string          `json:"occurred_start"`
	OccurredEnd        *string          `json:"occurred_end"`
	Entities           []string         `json:"entities"`
	DocumentID         *string          `json:"document_id"`
	ChunkID            *string          `json:"chunk_id"`
	Tags               []string         `json:"tags"`
	Metadata           map[string]any   `json:"metadata"`
	ObservationScopes  any              `json:"observation_scopes"`
	State              string           `json:"state"`
	InvalidationReason *string          `json:"invalidation_reason"`
	InvalidatedAt      *string          `json:"invalidated_at"`
	EditedAt           *string          `json:"edited_at"`
	History            []any            `json:"history,omitempty"`
	SourceMemoryIDs    []string         `json:"source_memory_ids,omitempty"`
	SourceMemories     []map[string]any `json:"source_memories,omitempty"`
	Attachments        []map[string]any `json:"attachments,omitempty"`
}

// memoryDetailPayload builds one unit's detail read, resolving the entity
// postings with the observation → source inheritance the detail view applies
// and, for an observation, the source facts it was folded from.
func (s *Server) memoryDetailPayload(ctx context.Context, bankID string, unit storage.MemoryUnit) (memoryDetail, error) {
	names, err := s.store.UnitEntityNames(ctx, bankID, []string{unit.ID})
	if err != nil {
		return memoryDetail{}, err
	}
	entities := names[unit.ID]
	if entities == nil {
		entities = []string{}
	}
	tags := unit.Tags
	if tags == nil {
		tags = []string{}
	}
	metadata := unit.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	date := ""
	if unit.EventDate != nil {
		date = httpx.FormatTimestamp(*unit.EventDate)
	}
	var scopes any
	if len(unit.ObservationScopes) > 0 {
		scopes = unit.ObservationScopes
	}
	detail := memoryDetail{
		ID: unit.ID, Text: unit.Text, Context: unit.Context, Date: date,
		Type: factTypeValue(unit.FactType), MentionedAt: httpx.FormatTimestampPtr(unit.MentionedAt),
		OccurredStart: httpx.FormatTimestampPtr(unit.OccurredStart),
		OccurredEnd:   httpx.FormatTimestampPtr(unit.OccurredEnd),
		Entities:      entities, DocumentID: optionalString(unit.DocumentID),
		ChunkID: optionalString(unit.ChunkID), Tags: tags, Metadata: metadata,
		ObservationScopes: scopes, State: memoryState(unit),
		InvalidationReason: unit.InvalidationReason,
		InvalidatedAt:      httpx.FormatTimestampPtr(unit.InvalidatedAt),
		EditedAt:           httpx.FormatTimestampPtr(unit.EditedAt),
	}
	if unit.FactType == storage.TypeObservation {
		detail.History = []any{}
		if len(unit.SourceMemoryIDs) > 0 {
			detail.SourceMemoryIDs = append([]string(nil), unit.SourceMemoryIDs...)
			sources, err := s.sourceMemories(ctx, bankID, detail.SourceMemoryIDs)
			if err != nil {
				return memoryDetail{}, err
			}
			detail.SourceMemories = sources
		}
	}
	if attachments, ok := s.store.(storage.MemoryAttachmentStore); ok {
		byUnit, err := attachments.MemoryAttachments(ctx, bankID, []string{unit.ID})
		if err != nil {
			return memoryDetail{}, err
		}
		if records := byUnit[unit.ID]; len(records) > 0 {
			detail.Attachments = memoryAttachmentItems(bankID, records)
		}
	}
	return detail, nil
}

// sourceMemories resolves an observation's source facts to summaries, newest
// mention first (nulls last), as the reference orders them.
func (s *Server) sourceMemories(ctx context.Context, bankID string, ids []string) ([]map[string]any, error) {
	reader, ok := s.store.(storage.SourceMemoryReader)
	if !ok {
		return []map[string]any{}, nil
	}
	rows, err := reader.SourceMemories(ctx, bankID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"id": row.ID, "text": row.Text, "type": sourceMemoryType(row.FactType),
			"context":        row.Context,
			"occurred_start": httpx.FormatTimestampPtr(row.OccurredStart),
			"mentioned_at":   httpx.FormatTimestampPtr(row.MentionedAt),
		})
	}
	return out, nil
}

// memoryAttachmentItems renders a memory's attachments in the reference's
// detail shape, with the bank-scoped URL that serves each one's bytes.
func memoryAttachmentItems(bankID string, records []storage.Attachment) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"id": record.ShortID, "hash": record.AttachmentHash, "kind": record.Kind,
			"media_type": record.MediaType, "byte_size": record.ByteSize,
			"filename": optionalString(record.Filename),
			"url":      "/v1/default/banks/" + bankID + "/attachments/" + record.ShortID,
		})
	}
	return out
}

// memoryState reports the unit's curation state from its invalidation record.
func memoryState(unit storage.MemoryUnit) string {
	if unit.InvalidatedAt != nil || unit.InvalidationReason != nil {
		return "invalidated"
	}
	return "valid"
}

// factTypeValue renders the unit's fact type for the detail payload, nully when
// the stored column carried none.
func factTypeValue(factType storage.MemoryUnitType) any {
	if factType == "" {
		return nil
	}
	return string(factType)
}

// sourceMemoryType renders a source fact's type, which stays null when the live
// column carried none.
func sourceMemoryType(factType string) any {
	if factType == "" {
		return nil
	}
	return factType
}

// ---------------------------------------------------------------------------
// Documents and tags

// documentListItem is one document as the reference's MCP listing renders it,
// the same row the HTTP listing serves.
type documentListItem struct {
	ID              string         `json:"id"`
	BankID          string         `json:"bank_id"`
	ContentHash     *string        `json:"content_hash"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	TextLength      int            `json:"text_length"`
	MemoryUnitCount int            `json:"memory_unit_count"`
	RetainParams    map[string]any `json:"retain_params"`
	Metadata        any            `json:"document_metadata"`
	Tags            []string       `json:"tags"`
	// MatchedFields names which fields a `q` search matched, empty without one.
	MatchedFields []string `json:"matched_fields"`
}

// documentListPage is the document listing envelope.
type documentListPage struct {
	Items  []documentListItem `json:"items"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
	// TotalIsEstimate reports that a `q` search stopped at its match cap.
	TotalIsEstimate bool `json:"total_is_estimate"`
}

// toDocumentListItem renders one stored document for the listing.
func toDocumentListItem(doc storage.Document) documentListItem {
	tags := doc.Tags
	if tags == nil {
		tags = []string{}
	}
	return documentListItem{
		ID: doc.ID, BankID: doc.BankID, ContentHash: optionalString(doc.ContentHash),
		CreatedAt: httpx.FormatTimestamp(doc.CreatedAt), UpdatedAt: httpx.FormatTimestamp(doc.UpdatedAt),
		TextLength: utf8.RuneCountInString(doc.OriginalText), MemoryUnitCount: doc.MemoryUnitCount,
		RetainParams: presentRetainParams(doc.RetainParams), Metadata: doc.Metadata, Tags: tags,
	}
}

// documentDetail is one document's detail read. The reference's MCP tool drops
// the attachment list the HTTP detail carries, so it is absent here too.
type documentDetail struct {
	ID                string         `json:"id"`
	BankID            string         `json:"bank_id"`
	OriginalText      *string        `json:"original_text"`
	ContentHash       *string        `json:"content_hash"`
	CreatedAt         string         `json:"created_at"`
	UpdatedAt         string         `json:"updated_at"`
	MemoryUnitCount   int            `json:"memory_unit_count"`
	Tags              []string       `json:"tags"`
	Metadata          any            `json:"document_metadata"`
	RetainParams      map[string]any `json:"retain_params"`
	NodesByFactType   map[string]int `json:"nodes_by_fact_type"`
	ObservationScopes any            `json:"observation_scopes"`
}

// documentDetailPayload builds one document's detail read.
func (s *Server) documentDetailPayload(ctx context.Context, bankID string, doc storage.Document) (documentDetail, error) {
	tags := doc.Tags
	if tags == nil {
		tags = []string{}
	}
	counts, err := s.store.DocumentFactCounts(ctx, bankID, doc.ID)
	if err != nil {
		return documentDetail{}, err
	}
	return documentDetail{
		ID: doc.ID, BankID: doc.BankID, OriginalText: optionalString(doc.OriginalText),
		ContentHash: optionalString(doc.ContentHash), CreatedAt: httpx.FormatTimestamp(doc.CreatedAt),
		UpdatedAt: httpx.FormatTimestamp(doc.UpdatedAt), MemoryUnitCount: doc.MemoryUnitCount,
		Tags: tags, Metadata: doc.Metadata, RetainParams: presentRetainParams(doc.RetainParams),
		NodesByFactType:   factTypeCounts(counts),
		ObservationScopes: presentObservationScopes(doc.RetainParams),
	}, nil
}

// factTypeCounts renders a document's per-type counts in the fixed breakdown
// the detail read reports: all three perspectives are always present, zero when
// the document has none.
func factTypeCounts(counts map[string]int) map[string]int {
	return map[string]int{
		"world":       counts["world"],
		"experience":  counts["experience"],
		"observation": counts["observation"],
	}
}

// presentObservationScopes returns the retain-time scope spec when it carries
// content; an absent or empty value reads as null.
func presentObservationScopes(retainParams map[string]any) any {
	if retainParams == nil {
		return nil
	}
	value, ok := retainParams["observation_scopes"]
	if !ok || value == nil {
		return nil
	}
	if scopes, ok := value.([]any); ok && len(scopes) == 0 {
		return nil
	}
	if text, ok := value.(string); ok && text == "" {
		return nil
	}
	return value
}

// tagItem is one tag with its usage count.
type tagItem struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// tagListPage is the tag listing envelope.
type tagListPage struct {
	Items  []tagItem `json:"items"`
	Total  int       `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

// presentRetainParams renders a document's replayable retain parameters, which
// read as null when the document carries none.
func presentRetainParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	return params
}

// ---------------------------------------------------------------------------
// Mental models and directives

// mentalModelItem is the metadata row the mental-model listing reports: the
// identity, the staleness verdict and the timestamps, never the content.
type mentalModelItem struct {
	ID                  string   `json:"id"`
	BankID              string   `json:"bank_id"`
	Name                string   `json:"name"`
	Tags                []string `json:"tags"`
	LastRefreshedAt     *string  `json:"last_refreshed_at"`
	LastMemorySeenAt    *string  `json:"last_memory_seen_at"`
	LastRefreshFailedAt *string  `json:"last_refresh_failed_at"`
	CreatedAt           string   `json:"created_at"`
	IsStale             *bool    `json:"is_stale"`
}

// mentalModelListPage is the mental-model listing envelope, which the engine
// reports without the page bounds the HTTP listing adds.
type mentalModelListPage struct {
	Items []mentalModelItem `json:"items"`
	Total int               `json:"total"`
}

// mentalModelItemFrom renders one stored model as a metadata row, with the
// caller-supplied staleness verdict the batch read computes. The row reports the
// same members as the single-model read at metadata detail, plus the verdict.
func mentalModelItemFrom(record storage.MentalModel, stale bool) mentalModelItem {
	tags := record.Tags
	if tags == nil {
		tags = []string{}
	}
	var created string
	if !record.CreatedAt.IsZero() {
		created = httpx.FormatTimestamp(record.CreatedAt)
	}
	return mentalModelItem{
		ID: record.ID, BankID: record.BankID, Name: record.Name, Tags: tags,
		LastRefreshedAt:     mentalModelTimestamp(record.LastRefreshedAt),
		LastMemorySeenAt:    httpx.FormatTimestampPtr(record.LastMemorySeenAt),
		LastRefreshFailedAt: httpx.FormatTimestampPtr(record.LastRefreshFailedAt),
		CreatedAt:           created,
		IsStale:             &stale,
	}
}

// mentalModelMetadata is one mental model at metadata detail: the identity, the
// tags and the timestamps, never the content.
type mentalModelMetadata struct {
	ID                  string   `json:"id"`
	BankID              string   `json:"bank_id"`
	Name                string   `json:"name"`
	Tags                []string `json:"tags"`
	LastRefreshedAt     *string  `json:"last_refreshed_at"`
	LastMemorySeenAt    *string  `json:"last_memory_seen_at"`
	LastRefreshFailedAt *string  `json:"last_refresh_failed_at"`
	CreatedAt           *string  `json:"created_at"`
}

// mentalModelContent is the model at content detail: the metadata plus how it is
// built and the synthesized body.
type mentalModelContent struct {
	mentalModelMetadata
	SourceQuery *string        `json:"source_query"`
	MaxTokens   *int           `json:"max_tokens"`
	Trigger     map[string]any `json:"trigger"`
	Content     *string        `json:"content"`
}

// mentalModelFull is the model at full detail: the content plus the stored
// reflect payload, the structured content it was rendered from and the
// staleness verdict the read computes.
type mentalModelFull struct {
	mentalModelContent
	ReflectResponse   map[string]any `json:"reflect_response"`
	StructuredContent map[string]any `json:"structured_content"`
	IsStale           *bool          `json:"is_stale,omitempty"`
}

// mentalModelAtDetail renders one stored model at the requested detail level,
// mirroring the reference's engine projection: metadata reports the identity and
// timestamps, content adds the configuration and the body, and full adds the
// reflect payload and the staleness verdict.
func mentalModelAtDetail(record storage.MentalModel, detail string, stale bool, withStaleness bool) any {
	tags := record.Tags
	if tags == nil {
		tags = []string{}
	}
	var created string
	if !record.CreatedAt.IsZero() {
		created = httpx.FormatTimestamp(record.CreatedAt)
	}
	base := mentalModelMetadata{
		ID: record.ID, BankID: record.BankID, Name: record.Name, Tags: tags,
		LastRefreshedAt:     mentalModelTimestamp(record.LastRefreshedAt),
		LastMemorySeenAt:    httpx.FormatTimestampPtr(record.LastMemorySeenAt),
		LastRefreshFailedAt: httpx.FormatTimestampPtr(record.LastRefreshFailedAt),
	}
	if created != "" {
		base.CreatedAt = &created
	}
	if detail == "metadata" {
		return base
	}
	content := mentalModelContent{mentalModelMetadata: base}
	sourceQuery := record.SourceQuery
	content.SourceQuery = &sourceQuery
	if record.MaxTokens > 0 {
		maxTokens := record.MaxTokens
		content.MaxTokens = &maxTokens
	}
	content.Trigger = record.Trigger
	body := record.Content
	content.Content = &body
	if detail != "full" {
		return content
	}
	full := mentalModelFull{
		mentalModelContent: content,
		ReflectResponse:    record.ReflectResult,
		StructuredContent:  record.Structured,
	}
	if withStaleness {
		full.IsStale = &stale
	}
	return full
}

// mentalModelTimestamp renders a model's refresh clock, which reads as null
// when the model has never refreshed.
func mentalModelTimestamp(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	rendered := httpx.FormatTimestamp(value)
	return &rendered
}

// directiveItem is one directive as the reference's MCP listing renders it, the
// same row the HTTP listing serves.
type directiveItem struct {
	ID        string   `json:"id"`
	BankID    string   `json:"bank_id"`
	Name      string   `json:"name"`
	Content   string   `json:"content"`
	Priority  int      `json:"priority"`
	IsActive  bool     `json:"is_active"`
	Tags      []string `json:"tags"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// directiveListPage is the directive listing envelope, which the engine reports
// without the page bounds the HTTP listing adds.
type directiveListPage struct {
	Items []directiveItem `json:"items"`
	Total int             `json:"total"`
}

// toDirectiveItem renders one stored directive.
func toDirectiveItem(record storage.Directive) directiveItem {
	tags := record.Tags
	if tags == nil {
		tags = []string{}
	}
	return directiveItem{
		ID: record.ID, BankID: record.BankID, Name: record.Name, Content: record.Content,
		Priority: record.Priority, IsActive: record.IsActive, Tags: tags,
		CreatedAt: httpx.FormatTimestamp(record.CreatedAt), UpdatedAt: httpx.FormatTimestamp(record.UpdatedAt),
	}
}

// sortDirectives orders directives highest priority first, ties newest-first
// then by identifier, which is the order the reference's listing returns and
// the order its pages are cut from.
func sortDirectives(records []storage.Directive) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Priority != records[j].Priority {
			return records[i].Priority > records[j].Priority
		}
		if !records[i].CreatedAt.Equal(records[j].CreatedAt) {
			return records[i].CreatedAt.After(records[j].CreatedAt)
		}
		return records[i].ID > records[j].ID
	})
}

// directiveMatchesTags reports whether a directive is in scope for the
// requested tags. An untagged (global) directive always applies; a tagged one
// must match the requested scope under the given mode.
func directiveMatchesTags(recordTags, want []string, mode string) bool {
	if len(recordTags) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(recordTags))
	for _, tag := range recordTags {
		have[tag] = struct{}{}
	}
	switch mode {
	case "all":
		for _, tag := range want {
			if _, ok := have[tag]; !ok {
				return false
			}
		}
		return true
	case "exact":
		if len(recordTags) != len(want) {
			return false
		}
		for _, tag := range want {
			if _, ok := have[tag]; !ok {
				return false
			}
		}
		return true
	default: // any
		for _, tag := range want {
			if _, ok := have[tag]; ok {
				return true
			}
		}
		return false
	}
}

// mentalModelMatchesTags reports whether a mental model is in scope for the
// requested tags, with the same untagged-applies rule the directives use.
func mentalModelMatchesTags(recordTags, want []string) bool {
	if len(want) == 0 {
		return true
	}
	if len(recordTags) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(recordTags))
	for _, tag := range recordTags {
		have[tag] = struct{}{}
	}
	for _, tag := range want {
		if _, ok := have[tag]; ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Operations and statistics

// operationListPage is the operation listing the engine reports: the rows and
// the total, without the bank identity and page bounds the HTTP listing adds.
type operationListPage struct {
	Total      int                   `json:"total"`
	Operations []memoryops.Operation `json:"operations"`
}

// operationNotFound is the single-operation read for an unknown identifier,
// which the reference reports as a normal result with status not_found.
type operationNotFound struct {
	OperationID   string  `json:"operation_id"`
	Status        string  `json:"status"`
	OperationType *string `json:"operation_type"`
	ID            string  `json:"id"`
	TaskType      *string `json:"task_type"`
	CreatedAt     *string `json:"created_at"`
	UpdatedAt     *string `json:"updated_at"`
	CompletedAt   *string `json:"completed_at"`
	ErrorMessage  *string `json:"error_message"`
}

// operationDetail is the engine's single-operation read: the status, the
// subject and outcome projection, the raw progress the operation recorded and
// the child roll-up a parent carries.
type operationDetail struct {
	OperationID     string           `json:"operation_id"`
	Status          string           `json:"status"`
	OperationType   *string          `json:"operation_type"`
	ID              string           `json:"id"`
	TaskType        *string          `json:"task_type"`
	MentalModelID   *string          `json:"mental_model_id"`
	CreatedAt       *string          `json:"created_at"`
	UpdatedAt       *string          `json:"updated_at"`
	CompletedAt     *string          `json:"completed_at"`
	ErrorMessage    *string          `json:"error_message"`
	RetryCount      int              `json:"retry_count"`
	NextRetryAt     *string          `json:"next_retry_at"`
	Progress        map[string]any   `json:"progress"`
	Details         map[string]any   `json:"details"`
	ResultMetadata  map[string]any   `json:"result_metadata"`
	ChildOperations []map[string]any `json:"child_operations,omitempty"`
	TaskPayload     map[string]any   `json:"task_payload"`
}

// operationDetailPayload renders one stored operation as the engine reports it.
// The subject and outcome projection comes from the same mapper the HTTP
// operation list uses, while progress stays the raw recorded object the engine
// hands back.
func operationDetailPayload(op storage.Operation, rawID string, children []storage.ChildOperation) operationDetail {
	list := memoryops.ToOperationListDTO(op)
	taskType, createdAt := list.TaskType, list.CreatedAt
	payload := operationDetail{
		OperationID: rawID, Status: string(op.Status), ID: rawID,
		OperationType: list.OperationType, TaskType: &taskType,
		MentalModelID: list.MentalModelID,
		CreatedAt:     &createdAt, UpdatedAt: list.UpdatedAt,
		CompletedAt:  httpx.FormatTimestampPtr(op.CompletedAt),
		ErrorMessage: list.ErrorMessage,
		RetryCount:   op.RetryCount, NextRetryAt: httpx.FormatTimestampPtr(op.NextRetryAt),
		Progress:       rawProgress(op.ResultMetadata),
		Details:        list.Details,
		ResultMetadata: resultMetadataValue(op.ResultMetadata),
	}
	if op.IsParent {
		payload.ChildOperations = make([]map[string]any, 0, len(children))
		for _, child := range children {
			payload.ChildOperations = append(payload.ChildOperations, map[string]any{
				"operation_id": child.OperationID, "status": string(child.Status),
				"sub_batch_index": child.SubBatchIndex, "items_count": child.ItemsCount,
				"error_message": child.ErrorMessage,
			})
		}
	}
	return payload
}

// rawProgress is the progress object the operation recorded, rendered as the
// engine's raw value rather than the HTTP projection that pads it.
func rawProgress(metadata map[string]any) map[string]any {
	if progress, ok := metadata["progress"].(map[string]any); ok {
		return progress
	}
	return nil
}

// resultMetadataValue renders result_metadata for a found operation: the map
// itself, or an empty object when the column carried none.
func resultMetadataValue(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

// bankStatsPayload is the statistics summary the engine reports, whose members
// are named for the engine's keys rather than the HTTP summary's.
type bankStatsPayload struct {
	BankID               string         `json:"bank_id"`
	NodeCounts           map[string]int `json:"node_counts"`
	LinkCounts           map[string]int `json:"link_counts"`
	LinkCountsByFactType map[string]any `json:"link_counts_by_fact_type"`
	LinkBreakdown        []any          `json:"link_breakdown"`
	Operations           map[string]int `json:"operations"`
	TotalDocuments       int            `json:"total_documents"`
	LastConsolidatedAt   *string        `json:"last_consolidated_at"`
	LastMemoryWriteAt    *string        `json:"last_memory_write_at"`
	PendingConsolidation int            `json:"pending_consolidation"`
	FailedConsolidation  int            `json:"failed_consolidation"`
	TotalObservations    int            `json:"total_observations"`
}

// newBankStatsPayload renders one bank's statistics in the engine's shape.
func newBankStatsPayload(stats storage.BankStats) bankStatsPayload {
	return bankStatsPayload{
		BankID:               stats.BankID,
		NodeCounts:           nonNilCounts(stats.NodesByFactType),
		LinkCounts:           nonNilCounts(stats.LinksByLinkType),
		LinkCountsByFactType: map[string]any{},
		LinkBreakdown:        []any{},
		Operations:           nonNilCounts(stats.OperationsByStatus),
		TotalDocuments:       stats.TotalDocuments,
		LastConsolidatedAt:   httpx.FormatTimestampPtr(stats.LastConsolidatedAt),
		LastMemoryWriteAt:    httpx.FormatTimestampPtr(stats.LastMemoryWriteAt),
		PendingConsolidation: stats.PendingConsolidation,
		FailedConsolidation:  stats.FailedConsolidation,
		TotalObservations:    stats.TotalObservations,
	}
}

// nonNilCounts renders a count histogram as an object, never null.
func nonNilCounts(counts map[string]int) map[string]int {
	if counts == nil {
		return map[string]int{}
	}
	return counts
}

// optionalString renders a possibly-empty string as a nullable field, so an
// absent value is a JSON null rather than an empty string.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// pageSlice cuts one page out of a listing, reporting an empty page for an
// offset at or past the end, as the reference's paging does.
func pageSlice[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return []T{}
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// ---------------------------------------------------------------------------
// Write acknowledgements
//
// Each of these is the payload the reference's engine returns from the tool's
// own operation, so the members and their order are the engine's rather than the
// HTTP route's. The tools that registered with indent=None return them compactly
// with CPython's default separators.

// clearMemoriesAck is the answer to clearing a bank's memory units. A clear
// narrowed to one fact type leaves the bank's entities and documents in place,
// and the engine reports only the unit count it removed; a full clear reports
// the documents it swept too.
type clearMemoriesAck struct {
	Status             string `json:"status"`
	BankID             string `json:"bank_id"`
	MemoryUnitsDeleted int    `json:"memory_units_deleted"`
	EntitiesDeleted    int    `json:"entities_deleted"`
	DocumentsDeleted   *int   `json:"documents_deleted,omitempty"`
}

// deleteBankAck is the answer to deleting a bank and its data.
type deleteBankAck struct {
	Status             string `json:"status"`
	BankID             string `json:"bank_id"`
	MemoryUnitsDeleted int    `json:"memory_units_deleted"`
	EntitiesDeleted    int    `json:"entities_deleted"`
	DocumentsDeleted   int    `json:"documents_deleted"`
	BankDeleted        bool   `json:"bank_deleted"`
}

// deleteDocumentAck is the answer to deleting a document.
type deleteDocumentAck struct {
	Status             string `json:"status"`
	DocumentID         string `json:"document_id"`
	DocumentDeleted    int    `json:"document_deleted"`
	MemoryUnitsDeleted int    `json:"memory_units_deleted"`
}

// deleteDirectiveAck is the answer to deleting a directive.
type deleteDirectiveAck struct {
	Status      string `json:"status"`
	DirectiveID string `json:"directive_id"`
}

// deleteMentalModelAck is the answer to deleting a mental model.
type deleteMentalModelAck struct {
	Status        string `json:"status"`
	MentalModelID string `json:"mental_model_id"`
}

// deleteKnowledgeNodeAck is the answer to deleting a knowledge node.
type deleteKnowledgeNodeAck struct {
	Status string `json:"status"`
	NodeID string `json:"node_id"`
}

// createMentalModelAck is the answer to creating a mental model, whose content
// is generated by the queued refresh the acknowledgement names.
type createMentalModelAck struct {
	MentalModelID string `json:"mental_model_id"`
	OperationID   string `json:"operation_id"`
	Status        string `json:"status"`
	Message       string `json:"message"`
}

// refreshMentalModelAck is the answer to queueing a mental-model refresh.
type refreshMentalModelAck struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Message     string `json:"message"`
}

// clearMentalModelAck is the answer to clearing a mental model's content.
type clearMentalModelAck struct {
	MentalModelID string `json:"mental_model_id"`
	Status        string `json:"status"`
	Message       string `json:"message"`
}

// cancelOperationAck is the answer to cancelling an operation.
type cancelOperationAck struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	OperationID string `json:"operation_id"`
	BankID      string `json:"bank_id"`
}

// ---------------------------------------------------------------------------
// Knowledge nodes

// knowledgeNodeView is one knowledge node as the reference's MCP tools render
// it: a folder carries its children and the time it last changed, a page its
// backing model's identity, description, tags, freshness and trigger.
type knowledgeNodeView struct {
	ID            string              `json:"id"`
	Kind          string              `json:"kind"`
	Name          string              `json:"name"`
	ParentID      *string             `json:"parent_id"`
	Managed       bool                `json:"managed"`
	MentalModelID *string             `json:"mental_model_id,omitempty"`
	Description   *string             `json:"description,omitempty"`
	Tags          []string            `json:"tags,omitempty"`
	Timestamp     *string             `json:"timestamp"`
	Trigger       map[string]any      `json:"trigger,omitempty"`
	IsStale       *bool               `json:"is_stale,omitempty"`
	Children      []knowledgeNodeView `json:"children,omitempty"`
}

// knowledgeNodePayload renders one stored node. A page reads its description,
// trigger and freshness from the model behind it; a folder carries structure
// only.
func (s *Server) knowledgeNodePayload(ctx context.Context, node storage.KnowledgeNode) (knowledgeNodeView, error) {
	out := knowledgeNodeView{
		ID: node.ID, Kind: string(node.Kind), Name: node.Title,
		Managed: node.Managed, ParentID: optionalString(node.ParentID),
	}
	if node.Kind != storage.KnowledgeKindPage {
		out.Timestamp = optionalTimestamp(node.UpdatedAt)
		return out, nil
	}
	out.MentalModelID = optionalString(node.MentalModelID)
	if node.Tags != nil {
		out.Tags = node.Tags
	} else {
		out.Tags = []string{}
	}
	model, err := s.store.GetMentalModel(ctx, node.BankID, node.MentalModelID)
	if err != nil {
		if !errors.Is(err, storage.ErrMentalModelNotFound) {
			return knowledgeNodeView{}, err
		}
		return out, nil
	}
	description := model.SourceQuery
	out.Description = &description
	out.Timestamp = mentalModelTimestamp(model.LastRefreshedAt)
	out.Trigger = model.Trigger
	stale := s.store.IsStale(ctx, node.BankID, model)
	out.IsStale = &stale
	return out, nil
}

// knowledgeTreePayload assembles a bank's flat node list into the nested roots
// the tree tool returns, in the order the listing yields them.
func (s *Server) knowledgeTreePayload(ctx context.Context, bankID string) ([]knowledgeNodeView, error) {
	nodeRows, err := s.store.ListKnowledgeNodes(ctx, bankID)
	if err != nil {
		return nil, err
	}
	projected := make(map[string]*knowledgeNodeView, len(nodeRows))
	for _, node := range nodeRows {
		entry, err := s.knowledgeNodePayload(ctx, node)
		if err != nil {
			return nil, err
		}
		copied := entry
		projected[node.ID] = &copied
	}
	roots := make([]knowledgeNodeView, 0)
	for _, node := range nodeRows {
		entry := projected[node.ID]
		parent, ok := projected[node.ParentID]
		// Only a folder can be a parent, so the parent normally carries a
		// children list; a row naming a parent that is not there stays a root.
		if node.ParentID != "" && ok && parent.Kind == string(storage.KnowledgeKindFolder) {
			if parent.Children == nil {
				parent.Children = []knowledgeNodeView{}
			}
			parent.Children = append(parent.Children, *entry)
			continue
		}
		roots = append(roots, *entry)
	}
	return roots, nil
}

// optionalTimestamp renders a time as the reference's isoformat, or nil when it
// is the zero value so the field serializes as JSON null.
func optionalTimestamp(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	rendered := httpx.FormatTimestamp(value)
	return &rendered
}

// uuidAnyForm reports whether value is a UUID in any form the reference's
// uuid.UUID() accepts: 32 hex digits, the 8-4-4-4-12 hyphenated form, either of
// those wrapped in braces, and the urn:uuid: prefixed form (case-insensitive).
func uuidAnyForm(value string) bool {
	trimmed := value
	if len(trimmed) >= 9 && strings.EqualFold(trimmed[:9], "urn:uuid:") {
		trimmed = trimmed[9:]
	}
	if len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' {
		trimmed = trimmed[1 : len(trimmed)-1]
	}
	trimmed = strings.ReplaceAll(trimmed, "-", "")
	if len(trimmed) != 32 {
		return false
	}
	for _, r := range trimmed {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
