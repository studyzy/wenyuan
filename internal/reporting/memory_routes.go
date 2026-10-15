package reporting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/consolidation"
	"github.com/studyzy/wenyuan/internal/extraction"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/reflect"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ListMemories returns one page of a bank's memory units.
func (h *Handlers) ListMemories(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
	if !containsString(tagsMatchModes, tagsMatch) {
		writeQueryEnumFailure(w, r, "tags_match", tagsMatch, tagsMatchModes)
		return
	}
	timeField := strings.TrimSpace(httpx.Query(r, "time_field"))
	if timeField != "" && !containsString(memoryTimeFields, timeField) {
		writeQueryEnumFailure(w, r, "time_field", timeField, memoryTimeFields)
		return
	}
	state := strings.TrimSpace(httpx.Query(r, "state"))
	if state != "" && state != "valid" && state != "invalidated" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid state '%s': expected 'valid' or 'invalidated'.", state)))
		return
	}
	consolidationState := strings.TrimSpace(httpx.Query(r, "consolidation_state"))
	if consolidationState != "" && !containsString(consolidationStates, consolidationState) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid consolidation_state '%s': expected 'failed', 'pending', or 'done'.", consolidationState)))
		return
	}
	entityID := strings.TrimSpace(httpx.Query(r, "entity_id"))
	if entityID != "" && !validUUIDShape(entityID) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid entity_id: '%s' is not a valid UUID", entityID)))
		return
	}
	start, ok := parseDateParam(w, r, "start_date")
	if !ok {
		return
	}
	end, ok := parseDateParam(w, r, "end_date")
	if !ok {
		return
	}
	limit, ok := nonNegativeIntParam(w, r, "limit", 100)
	if !ok {
		return
	}
	offset, ok := nonNegativeIntParam(w, r, "offset", 0)
	if !ok {
		return
	}
	// The caller's resolved read scope is AND-ed into the tag filter, so a
	// scoped reader pages over the memories it may read and the total agrees.
	tagGroups, tags, tagsMatch := memoryops.ApplyMemoryTagScope(r, httpx.QueryValues(r, "tags"), tagsMatch)
	page, err := h.Store.ListMemories(r.Context(), storage.MemoryQuery{
		BankID:             bankID,
		Type:               strings.TrimSpace(httpx.Query(r, "type")),
		Q:                  strings.TrimSpace(httpx.Query(r, "q")),
		DocumentID:         strings.TrimSpace(httpx.Query(r, "document_id")),
		EntityID:           entityID,
		Tags:               tags,
		TagsMatch:          tagsMatch,
		TagGroups:          tagGroups,
		State:              state,
		ConsolidationState: consolidationState,
		TimeField:          timeField,
		TimeStart:          start,
		TimeEnd:            end,
		Limit:              limit, Offset: offset,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]MemoryItem, 0, len(page.Memories))
	for _, unit := range page.Memories {
		items = append(items, toMemoryItem(unit))
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, MemoryListResponse{
		Items: items, Total: page.Total, Limit: limit, Offset: offset,
	})
}

// tagsMatchModes are the accepted tags_match values.
var tagsMatchModes = []string{"any", "all", "any_strict", "all_strict", "exact"}

// memoryTimeFields are the accepted time_field values.
var memoryTimeFields = []string{"created_at", "updated_at", "mentioned_at", "occurred_start", "occurred_end"}

// consolidationStates are the accepted consolidation_state values.
var consolidationStates = []string{"failed", "pending", "done"}

// nonNegativeIntParam reads an optional integer query parameter with a default,
// rejecting a malformed or negative value with the framework's 422 body.
func nonNegativeIntParam(w http.ResponseWriter, r *http.Request, name string, def int) (int, bool) {
	raw := strings.TrimSpace(httpx.Query(r, name))
	if raw == "" {
		return def, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.TypeMismatch("int_parsing", []string{"query", name}, raw),
		}})
		return 0, false
	}
	if value < 0 {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.GreaterThanEqual([]string{"query", name}, value, 0),
		}})
		return 0, false
	}
	return value, true
}

// containsString reports whether values holds target.
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// validUUIDShape reports whether value has the canonical 8-4-4-4-12 hex form.
func validUUIDShape(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHexRune(r) {
				return false
			}
		}
	}
	return true
}

// isHexRune reports whether r is a hexadecimal digit.
func isHexRune(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// GetMemory returns one memory unit's detail.
func (h *Handlers) GetMemory(w http.ResponseWriter, r *http.Request) {
	bankID, memoryID, ok := h.memoryMember(w, r)
	if !ok {
		return
	}
	unit, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// A memory outside the caller's read scope reads as not-found, so the answer
	// never confirms the id exists.
	if !storage.TagsVisibleUnderScope(unit.Tags, memoryops.ScopeFromRequest(r)) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "memory not found"))
		return
	}
	detail, err := h.memoryDetail(r.Context(), bankID, unit)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, detail)
}

// UpdateMemory applies a partial update to one memory unit.
func (h *Handlers) UpdateMemory(w http.ResponseWriter, r *http.Request) {
	bankID, memoryID, ok := h.memoryMember(w, r)
	if !ok {
		return
	}
	var body MemoryUpdateRequest
	if err := decodeBody(r, &body); err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	if failure := validateMemoryUpdate(body); failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	// Reading a memory does not make it the caller's to change: an out-of-scope
	// memory reads as not-found, and a visible one whose tags the caller may not
	// write is refused with 403 rather than a lie about its existence.
	existing, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	if !storage.TagsVisibleUnderScope(existing.Tags, memoryops.ScopeFromRequest(r)) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "memory not found"))
		return
	}
	if writeTags, confined := memoryops.WriteScopeFromRequest(r); confined && !httpx.TagsWritable(existing.Tags, writeTags, confined) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindForbidden,
			"You can't write a memory tagged "+strings.Join(existing.Tags, ", ")))
		return
	}
	unit, err := h.Store.UpdateMemory(r.Context(), bankID, memoryID, storage.MemoryPatch{
		Text: body.Text, Context: body.Context,
		OccurredStart: body.OccurredStart, OccurredEnd: body.OccurredEnd,
		FactType: body.FactType, Entities: body.Entities, ResolveEntities: body.ResolveEntities,
		State: body.State, Reason: body.Reason,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	detail, err := h.memoryDetail(r.Context(), bankID, unit)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, detail)
}

// validateMemoryUpdate mirrors the reference's UpdateMemoryRequest constraints:
// at least one editable field, and a curation state within its enum.
func validateMemoryUpdate(body MemoryUpdateRequest) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	if body.Text == nil && body.Context == nil && body.OccurredStart == nil &&
		body.OccurredEnd == nil && body.FactType == nil && body.Entities == nil && body.State == nil {
		items = append(items, httpx.ValueError([]string{"body"}, nil, "Provide at least one field to update."))
	}
	if body.State != nil && *body.State != "valid" && *body.State != "invalidated" {
		items = append(items, httpx.ValueError([]string{"body"}, *body.State,
			"state must be 'valid' or 'invalidated'."))
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}

// MemoryHistory returns the recorded changes of one memory unit as the bare
// array the reference emits.
func (h *Handlers) MemoryHistory(w http.ResponseWriter, r *http.Request) {
	bankID, memoryID, ok := h.memoryMember(w, r)
	if !ok {
		return
	}
	unit, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// Only observations carry a history; a source fact reports an empty array.
	if unit.FactType != storage.TypeObservation {
		httpx.WriteJSONOpenRow(w, r, http.StatusOK, []MemoryHistoryEntry{})
		return
	}
	entries, err := h.Store.MemoryHistory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	history := make([]MemoryHistoryEntry, 0, len(entries))
	for _, entry := range entries {
		sourceFacts := make([]SourceFactModel, 0, len(entry.NewSourceMemoryIDs))
		for _, id := range entry.NewSourceMemoryIDs {
			sourceFacts = append(sourceFacts, SourceFactModel{ID: id, IsNew: true})
		}
		newIDs := entry.NewSourceMemoryIDs
		if newIDs == nil {
			newIDs = []string{}
		}
		previousTags := entry.PreviousTags
		if previousTags == nil {
			previousTags = []string{}
		}
		changedAt := formatTime(entry.ChangedAt)
		history = append(history, MemoryHistoryEntry{
			PreviousText:          entry.PreviousText,
			PreviousTags:          previousTags,
			PreviousOccurredStart: formatTimePtr(entry.PreviousOccurredStart),
			PreviousOccurredEnd:   formatTimePtr(entry.PreviousOccurredEnd),
			PreviousMentionedAt:   formatTimePtr(entry.PreviousMentionedAt),
			ChangedAt:             &changedAt,
			NewSourceMemoryIDs:    newIDs,
			SourceFacts:           sourceFacts,
		})
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, history)
}

// memoryDetail renders the reference's memory-unit detail dict.
func (h *Handlers) memoryDetail(ctx context.Context, bankID string, unit storage.MemoryUnit) (map[string]any, error) {
	entities, err := h.Store.UnitEntities(ctx, bankID, unit.ID)
	if err != nil {
		return nil, err
	}
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
		date = formatTime(*unit.EventDate)
	}
	var observationScopes json.RawMessage
	if len(unit.ObservationScopes) > 0 {
		observationScopes = unit.ObservationScopes
	}
	detail := map[string]any{
		"id": unit.ID, "text": unit.Text, "context": unit.Context, "date": date,
		"type": factTypeValue(unit.FactType), "mentioned_at": formatTimePtr(unit.MentionedAt),
		"occurred_start": formatTimePtr(unit.OccurredStart),
		"occurred_end":   formatTimePtr(unit.OccurredEnd),
		"entities":       entities, "document_id": nullableString(unit.DocumentID),
		"chunk_id": nullableString(unit.ChunkID), "tags": tags, "metadata": metadata,
		"observation_scopes": observationScopes, "state": memoryState(unit),
		"invalidation_reason": unit.InvalidationReason,
		"invalidated_at":      formatTimePtr(unit.InvalidatedAt),
		"edited_at":           formatTimePtr(unit.EditedAt),
	}
	if unit.FactType == storage.TypeObservation {
		detail["history"] = []any{}
		if len(unit.SourceMemoryIDs) > 0 {
			sourceIDs := append([]string(nil), unit.SourceMemoryIDs...)
			detail["source_memory_ids"] = sourceIDs
			sources, err := h.sourceMemories(ctx, bankID, sourceIDs)
			if err != nil {
				return nil, err
			}
			detail["source_memories"] = sources
		}
	}
	return detail, nil
}

// factTypeValue renders the unit's fact type for the detail dict.
func factTypeValue(factType storage.MemoryUnitType) any {
	if factType == "" {
		return nil
	}
	return string(factType)
}

// sourceMemories resolves an observation's source facts to summaries, newest
// mention first (nulls last), as the reference orders them.
func (h *Handlers) sourceMemories(ctx context.Context, bankID string, ids []string) ([]map[string]any, error) {
	type resolved struct {
		mentioned *time.Time
		id        string
		value     map[string]any
	}
	rows := make([]resolved, 0, len(ids))
	for _, id := range ids {
		unit, err := h.Store.GetMemory(ctx, bankID, id)
		if err != nil {
			if errors.Is(err, storage.ErrMemoryNotFound) {
				rows = append(rows, resolved{id: id, value: map[string]any{"id": id, "text": nil, "type": nil,
					"context": nil, "occurred_start": nil, "mentioned_at": nil}})
				continue
			}
			return nil, err
		}
		rows = append(rows, resolved{mentioned: unit.MentionedAt, id: id, value: map[string]any{
			"id": unit.ID, "text": unit.Text, "type": string(unit.FactType),
			"context": unit.Context, "occurred_start": formatTimePtr(unit.OccurredStart),
			"mentioned_at": formatTimePtr(unit.MentionedAt),
		}})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i].mentioned, rows[j].mentioned
		switch {
		case left == nil && right == nil:
			return rows[i].id < rows[j].id
		case left == nil:
			return false
		case right == nil:
			return true
		default:
			if !left.Equal(*right) {
				return left.After(*right)
			}
			return rows[i].id < rows[j].id
		}
	})
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.value)
	}
	return out, nil
}

// dryRunExtractRequest is the dry-run body. Everything below the content is a
// prompt-affecting override applied for this call only.
type dryRunExtractRequest struct {
	Content                  ContentInput     `json:"content"`
	Context                  string           `json:"context"`
	Timestamp                *string          `json:"timestamp"`
	AgentName                *string          `json:"agent_name"`
	Strategy                 *string          `json:"strategy"`
	RetainMission            *string          `json:"retain_mission"`
	RetainExtractionMode     *string          `json:"retain_extraction_mode"`
	RetainCustomInstructions *string          `json:"retain_custom_instructions"`
	RetainExtractCausalLinks *bool            `json:"retain_extract_causal_links"`
	RetainChunkSize          *int             `json:"retain_chunk_size"`
	EntityLabels             []map[string]any `json:"entity_labels"`
	EntitiesAllowFreeForm    *bool            `json:"entities_allow_free_form"`
	LLMOutputLanguage        *string          `json:"llm_output_language"`
}

// DryRunExtract reports what a retain would extract, without storing anything.
func (h *Handlers) DryRunExtract(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireBank(w, r); !ok {
		return
	}
	var body dryRunExtractRequest
	if err := decodeBody(r, &body); err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	contentValue := body.Content.Content()
	contentText := contentValue.Flatten()
	if strings.TrimSpace(contentText) == "" {
		httpx.WriteValidationFor(w, r, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body", "content"}, map[string]any{}),
		}})
		return
	}
	options := storage.FactExtractionOptions{}
	if body.RetainMission != nil {
		options.Mission = *body.RetainMission
	}
	if body.RetainExtractionMode != nil {
		options.Mode = *body.RetainExtractionMode
	}
	if body.RetainCustomInstructions != nil {
		options.Instructions = *body.RetainCustomInstructions
	}
	if body.Strategy != nil {
		options.Strategy = *body.Strategy
	}
	if body.Timestamp != nil {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*body.Timestamp)); err == nil {
			options.ReferenceTime = parsed
		}
	}

	inline := contentValue.InlineAttachments()
	byBlock := make(map[int]memoryops.InlineAttachment, len(inline))
	for _, attachment := range inline {
		byBlock[attachment.BlockIndex] = attachment
	}

	chunkSize := 0
	if body.RetainChunkSize != nil {
		chunkSize = *body.RetainChunkSize
	}
	segments := storage.SegmentWith(contentText, storage.SegmentOptions{
		ChunkSize: chunkSize, StructuredChunkSize: chunkSize,
	})
	segmentOccurrences := occurrencesBySegment(segments, dryRunOccurrences(inline))

	facts := make([]ExtractedFactModel, 0)
	chunks := make([]ExtractionChunk, 0, len(segments))
	usage := storage.TokenUsage{}
	for index, segment := range segments {
		occurrences := segmentOccurrences[index]
		result := storage.DryRunExtraction{}
		if h.Extraction != nil {
			options.Attachments = attachmentsForSegment(occurrences, byBlock)
			extracted, err := extractDryRun(r.Context(), h.Extraction, segment, options)
			if err != nil {
				httpx.WriteErrorFor(w, r, asHTTPError(err))
				return
			}
			result = extracted
			for _, fact := range result.Facts {
				chunkIndex := index
				facts = append(facts, ExtractedFactModel{
					FactType:      extractedFactType(fact.Type),
					Text:          fact.Text,
					OccurredStart: nullableString(strings.TrimSpace(fact.OccurredStart)),
					OccurredEnd:   nullableString(strings.TrimSpace(fact.OccurredEnd)),
					Entities:      extractedEntityNames(fact),
					ChunkIndex:    &chunkIndex,
					Attachments:   attachmentsForFact(fact.FromAttachments, occurrences),
				})
			}
			usage = addTokenUsage(usage, result.Usage)
		}
		chunks = append(chunks, ExtractionChunk{
			Text: renderChunkPlaceholders(segment, occurrences), FactCount: len(result.Facts),
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, DryRunExtractionResult{
		Facts: facts, Chunks: chunks, Usage: TokenUsageModel{
			InputTokens:    usage.InputTokens,
			OutputTokens:   usage.OutputTokens,
			TotalTokens:    usage.TotalTokens,
			CachedTokens:   usage.CachedTokens,
			ThoughtsTokens: usage.ThoughtsTokens,
		},
	})
}

// extractDryRun runs the configured extractor for the dry-run preview, taking
// the usage-reporting call when the extractor offers one and falling back to the
// facts-only extraction otherwise.
func extractDryRun(ctx context.Context, extractor storage.FactExtraction, content string, options storage.FactExtractionOptions) (storage.DryRunExtraction, error) {
	if dry, ok := extractor.(storage.DryRunFactExtraction); ok {
		return dry.ExtractDryRun(ctx, content, options)
	}
	facts, err := extractFacts(ctx, extractor, content, options)
	return storage.DryRunExtraction{Facts: facts}, err
}

// extractFacts runs the configured extractor, using the richer call when the
// request carried overrides the extractor can honor.
func extractFacts(ctx context.Context, extractor storage.FactExtraction, content string, options storage.FactExtractionOptions) ([]storage.ExtractedFact, error) {
	if contextual, ok := extractor.(storage.ContextualFactExtraction); ok {
		return contextual.ExtractWithOptions(ctx, content, options)
	}
	return extractor.Extract(ctx, content)
}

// extractedFactType resolves the fact's perspective, defaulting an unset one to
// world as the retain path does.
func extractedFactType(kind string) string {
	trimmed := strings.TrimSpace(kind)
	if trimmed == "" {
		return "world"
	}
	return trimmed
}

// extractedEntityNames lists the names the extractor reported.
func extractedEntityNames(fact storage.ExtractedFact) []string {
	names := make([]string, 0, len(fact.Entities))
	for _, entity := range fact.Entities {
		if strings.TrimSpace(entity.Text) != "" {
			names = append(names, entity.Text)
		}
	}
	return names
}

// PromptPreview reports the prompt a retain or reflect run would use, without
// running it.
func (h *Handlers) PromptPreview(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var body PromptPreviewRequest
	if err := decodeBody(r, &body); err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	operation := strings.TrimSpace(body.Operation)
	if operation == "" {
		operation = "retain"
	}
	directives := h.directivesFor(r.Context(), bankID)
	messages, schema, strategies, err := renderPromptPreview(r.Context(), h, bankID, operation, directives)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, PromptPreviewResponse{
		Messages:       messages,
		Strategy:       body.Strategy,
		Strategies:     strategies,
		RunSettings:    []RunSettingModel{},
		ResponseSchema: schema,
	})
}

// promptPreviewOperations lists the operations the preview can render, in the
// order the reference reports them.
var promptPreviewOperations = []string{"retain", "consolidation", "reflect"}

// renderPromptPreview renders one operation's messages and response schema from
// the same builders the real operation calls, so a preview cannot drift from
// what is sent. Each message is reported as the single built-in block it is
// built from.
func renderPromptPreview(ctx context.Context, h *Handlers, bankID, operation string, directives []string) ([]PromptPreviewMessage, any, []string, error) {
	toPreview := func(messages []provider.Message) []PromptPreviewMessage {
		out := make([]PromptPreviewMessage, 0, len(messages))
		for _, message := range messages {
			out = append(out, PromptPreviewMessage{
				Role: string(message.Role),
				Blocks: []PromptBlockModel{{
					Text: message.Content, Source: "builtin", Active: true, Kind: "text",
				}},
			})
		}
		return out
	}
	switch operation {
	case "retain":
		messages, schema := extraction.PreviewMessagesFor(h.retainPreviewOptions(ctx, bankID))
		return toPreview(messages), schema, promptPreviewOperations, nil
	case "consolidation":
		messages, schema := consolidation.PreviewMessages()
		return toPreview(messages), schema, promptPreviewOperations, nil
	case "reflect":
		return toPreview(reflect.PreviewMessages(directives)), nil, promptPreviewOperations, nil
	default:
		return nil, nil, promptPreviewOperations,
			fmt.Errorf("unknown preview operation %q", operation)
	}
}

// directivesFor returns the bank's standing directives, so the reflect preview
// shows the section the real run would add.
func (h *Handlers) directivesFor(ctx context.Context, bankID string) []string {
	if strings.TrimSpace(bankID) == "" {
		return nil
	}
	directives, err := h.Store.ListDirectives(ctx, bankID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(directives))
	for _, directive := range directives {
		if strings.TrimSpace(directive.Content) != "" {
			out = append(out, directive.Content)
		}
	}
	return out
}

// retainPreviewOptions reads the bank's retain configuration so the previewed
// prompt is the one a retain for that bank would send: the mode selects the
// prompt and schema, and the custom instructions are consulted only in custom
// mode, which the prompt builder enforces.
func (h *Handlers) retainPreviewOptions(ctx context.Context, bankID string) extraction.Options {
	config, err := h.Store.BankConfig(ctx, bankID)
	if err != nil {
		config = nil
	}
	return extraction.Options{
		Mode:                   configString(config, "retain_extraction_mode"),
		Mission:                configString(config, "retain_mission"),
		Instructions:           configString(config, "retain_custom_instructions"),
		OptionalFactDimensions: configBool(config, "retain_optional_fact_dimensions"),
	}
}

// configString reads a string override, defaulting to empty.
func configString(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return value
}

// configBool reads a boolean override, defaulting to false.
func configBool(config map[string]any, key string) bool {
	switch value := config[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}
