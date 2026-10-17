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

	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/extraction"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/reflect"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ListMemories returns one page of a bank's memory units.
func (h *Handlers) ListMemories(w http.ResponseWriter, r *http.Request) {
	// The reference validates the typed query before the handler (and its bank
	// lookup) runs, so an out-of-enum or malformed parameter is the answer even
	// when the bank is unknown.
	query := r.URL.Query()
	if _, present := query["tags_match"]; present {
		tagsMatch := httpx.Query(r, "tags_match")
		if !containsString(tagsMatchModes, tagsMatch) {
			writeQueryEnumFailure(w, r, "tags_match", tagsMatch, tagsMatchModes)
			return
		}
	}
	if _, present := query["time_field"]; present {
		timeField := httpx.Query(r, "time_field")
		if !containsString(memoryTimeFields, timeField) {
			writeQueryEnumFailure(w, r, "time_field", timeField, memoryTimeFields)
			return
		}
	}
	if _, present := query["state"]; present {
		state := httpx.Query(r, "state")
		if state != "valid" && state != "invalidated" {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
				fmt.Sprintf("Invalid state '%s': expected 'valid' or 'invalidated'.", state)))
			return
		}
	}
	rawConsolidationState := httpx.Query(r, "consolidation_state")
	consolidationState := strings.ToLower(rawConsolidationState)
	if consolidationState != "" && !containsString(consolidationStates, consolidationState) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid consolidation_state '%s': expected 'failed', 'pending', or 'done'.", rawConsolidationState)))
		return
	}
	entityID := httpx.Query(r, "entity_id")
	if _, present := query["entity_id"]; present && !uuidAnyForm(entityID) {
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
	// The window is half-open [start, end): an inverted window is a bad request,
	// not a silently empty page.
	if start != nil && end != nil && !end.After(*start) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"Invalid time window: end_date must be after start_date."))
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
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	tagsMatch := httpx.Query(r, "tags_match")
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
	state := httpx.Query(r, "state")
	timeField := httpx.Query(r, "time_field")
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
	unitIDs := make([]string, 0, len(page.Memories))
	for _, unit := range page.Memories {
		unitIDs = append(unitIDs, unit.ID)
	}
	// Entities are postings rather than a column on the unit, so one lookup
	// answers for the whole page.
	entityNames, err := h.Store.UnitEntityNames(r.Context(), bankID, unitIDs)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]MemoryItem, 0, len(page.Memories))
	for _, unit := range page.Memories {
		item := toMemoryItem(unit)
		if names, ok := entityNames[unit.ID]; ok {
			item.Entities = strings.Join(names, ", ")
		}
		items = append(items, item)
	}
	// Attachments are the ones each fact was drawn from, resolved for the whole
	// page in one lookup; an observation shows its source facts'. The key stays
	// off an item that has none.
	if attachmentStore, ok := h.Store.(storage.MemoryAttachmentStore); ok {
		byUnit, err := attachmentStore.MemoryAttachments(r.Context(), bankID, unitIDs)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		for index := range items {
			if records := byUnit[items[index].ID]; len(records) > 0 {
				items[index].Attachments = memoryAttachmentItems(bankID, records)
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, MemoryListResponse{
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
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.TypeMismatch("int_parsing", []string{"query", name}, raw),
		}})
		return 0, false
	}
	if value < 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.GreaterThanEqual([]string{"query", name}, raw, 0),
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

// uuidAnyForm reports whether value is a UUID in any form the reference's
// uuid.UUID() accepts: 32 hex digits, the 8-4-4-4-12 hyphenated form, either of
// those wrapped in braces, and the urn:uuid: prefixed form (case-insensitive).
func uuidAnyForm(value string) bool {
	s := value
	if len(s) >= 9 && strings.EqualFold(s[:9], "urn:uuid:") {
		s = s[9:]
	}
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		s = s[1 : len(s)-1]
	}
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !isHexRune(r) {
			return false
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
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	memoryID := strings.TrimSpace(r.PathValue("memory_id"))
	// The reference rejects a malformed id before touching the store; a missing
	// bank is not checked at all, so an unknown bank reads as a missing unit.
	if !uuidAnyForm(memoryID) {
		httpx.WriteErrorFor(w, r, memoryIDInvalid(memoryID))
		return
	}
	unit, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, memoryLookupError(err, memoryID))
		return
	}
	// A memory outside the caller's read scope reads as not-found, so the answer
	// never confirms the id exists.
	if !storage.TagsVisibleUnderScope(unit.Tags, memoryops.ScopeFromRequest(r)) {
		httpx.WriteErrorFor(w, r, memoryUnitNotFound(memoryID))
		return
	}
	detail, err := h.memoryDetail(r.Context(), bankID, unit)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

// memoryIDInvalid is the reference's 400 for a memory identifier that is not a
// UUID.
func memoryIDInvalid(memoryID string) *httpx.HTTPError {
	return httpx.NewHTTPError(httpx.KindBadRequest,
		fmt.Sprintf("Invalid memory_id: '%s' is not a valid UUID", memoryID))
}

// memoryUnitNotFound is the reference's 404 detail, naming the identifier the
// client sent.
func memoryUnitNotFound(memoryID string) *httpx.HTTPError {
	return httpx.NewHTTPError(httpx.KindNotFound, fmt.Sprintf("Memory unit '%s' not found", memoryID))
}

// memoryLookupError maps a store lookup failure: a missing unit becomes the
// reference's named 404, anything else keeps its own status.
func memoryLookupError(err error, memoryID string) *httpx.HTTPError {
	if errors.Is(err, storage.ErrMemoryNotFound) {
		return memoryUnitNotFound(memoryID)
	}
	return asHTTPError(err)
}

// UpdateMemory applies a partial update to one memory unit.
func (h *Handlers) UpdateMemory(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	memoryID := strings.TrimSpace(r.PathValue("memory_id"))
	var body MemoryUpdateRequest
	raw, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	if len(raw) == 0 {
		writeRequiredBodyFailure(w, r, fmt.Errorf("request body must be JSON: empty body"))
		return
	}
	if _, err := httpx.DecodeLax(raw, &body); err != nil {
		writeRequiredBodyFailure(w, r, fmt.Errorf("request body must be JSON: %w", err))
		return
	}
	httpx.RecordUnknownBodyFields(r, raw, &body)
	fields := bodyFieldPresence(raw)
	if failure := validateMemoryUpdate(body, fields, bodyInput(raw)); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	if !uuidAnyForm(memoryID) {
		httpx.WriteErrorFor(w, r, memoryIDInvalid(memoryID))
		return
	}
	if body.Text != nil && strings.TrimSpace(*body.Text) == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "text must not be empty."))
		return
	}
	// Reading a memory does not make it the caller's to change: an out-of-scope
	// memory reads as not-found, and a visible one whose tags the caller may not
	// write is refused with 403 rather than a lie about its existence.
	existing, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, memoryLookupError(err, memoryID))
		return
	}
	if existing.FactType != storage.TypeWorld && existing.FactType != storage.TypeExperience {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Memory '%s' is a %s; only world/experience facts can be curated. "+
				"Observations are derived and regenerate from their sources.", memoryID, existing.FactType)))
		return
	}
	doingEdit := body.Text != nil || body.Context != nil || fields["occurred_start"] || fields["occurred_end"] ||
		body.FactType != nil || body.Entities != nil
	if doingEdit && existing.InvalidatedAt != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"Cannot edit an invalidated memory; revert it to 'valid' first."))
		return
	}
	if !storage.TagsVisibleUnderScope(existing.Tags, memoryops.ScopeFromRequest(r)) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound,
			fmt.Sprintf("Memory '%s' not found", memoryID)))
		return
	}
	if writeTags, confined := memoryops.WriteScopeFromRequest(r); !writableUnderScope(existing.Tags, writeTags, confined) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindForbidden,
			fmt.Sprintf("You can't write memory '%s' tagged %s",
				memoryID, strings.Join(unwritableTags(existing.Tags, writeTags), ", "))))
		return
	}
	// The dates are parsed after the scope checks, as the reference does: a
	// caller who may not change the memory hears about that first.
	for _, rawDate := range []*string{body.OccurredStart, body.OccurredEnd} {
		if rawDate != nil && *rawDate != "" {
			if _, err := parseISODateTime(*rawDate); err != nil {
				httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
					fmt.Sprintf("Invalid isoformat string: '%s'", *rawDate)))
				return
			}
		}
	}
	unit, err := h.Store.UpdateMemory(r.Context(), bankID, memoryID, storage.MemoryPatch{
		Text: body.Text, Context: body.Context,
		OccurredStart: body.OccurredStart, OccurredEnd: body.OccurredEnd,
		ClearOccurredStart: fields["occurred_start"] && body.OccurredStart == nil,
		ClearOccurredEnd:   fields["occurred_end"] && body.OccurredEnd == nil,
		FactType:           body.FactType, Entities: body.Entities, ResolveEntities: body.ResolveEntities,
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
	httpx.WriteJSON(w, http.StatusOK, detail)
}

// requireWritableMemory enforces the reference's `_require_writable` for a
// by-id memory write: outside the caller's read scope the memory reads as
// not-found, and a visible one whose tags the caller may not write is refused
// with 403. A missing memory inside a scope reads the same as one hidden by it.
// Without either scope it is a no-op, leaving the operation's own handling of a
// missing memory untouched.
func (h *Handlers) requireWritableMemory(w http.ResponseWriter, r *http.Request, bankID, memoryID string) bool {
	readScope := memoryops.ScopeFromRequest(r)
	writeScope, confined := memoryops.WriteScopeFromRequest(r)
	if len(readScope) == 0 && !confined {
		return true
	}
	unit, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		if errors.Is(err, storage.ErrMemoryNotFound) {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound,
				fmt.Sprintf("Memory '%s' not found", memoryID)))
			return false
		}
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return false
	}
	if !storage.TagsVisibleUnderScope(unit.Tags, readScope) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound,
			fmt.Sprintf("Memory '%s' not found", memoryID)))
		return false
	}
	if !writableUnderScope(unit.Tags, writeScope, confined) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindForbidden,
			fmt.Sprintf("You can't write memory '%s' tagged %s",
				memoryID, strings.Join(unwritableTags(unit.Tags, writeScope), ", "))))
		return false
	}
	return true
}

// writableUnderScope reports whether a confined writer may change an item with
// these tags. An untagged item belongs to everyone, so a restricted writer may
// not change one — the reference's own rule.
func writableUnderScope(tags, writeScope []string, confined bool) bool {
	if !confined {
		return true
	}
	if len(tags) == 0 {
		return false
	}
	return httpx.TagsWritable(tags, writeScope, confined)
}

// unwritableTags lists, sorted, the tags a confined writer may not write, or the
// reference's "(untagged)" placeholder for an item that carries none.
func unwritableTags(tags, writeScope []string) []string {
	outside := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !httpx.TagsWritable([]string{tag}, writeScope, true) {
			outside = append(outside, tag)
		}
	}
	sort.Strings(outside)
	if len(outside) == 0 {
		return []string{"(untagged)"}
	}
	return outside
}

// bodyFieldPresence reports which top-level fields the caller actually sent,
// which is how the reference distinguishes "clear this field" (an explicit
// JSON null) from "leave it unchanged" (an omitted field).
func bodyFieldPresence(raw []byte) map[string]bool {
	present := map[string]bool{}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return present
	}
	for key := range fields {
		present[key] = true
	}
	return present
}

// bodyInput decodes the request body into the JSON value the reference echoes as
// a validation error's offending input.
func bodyInput(raw []byte) any {
	var input any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil
	}
	return input
}

// validateMemoryUpdate mirrors the reference's UpdateMemoryRequest constraints:
// at least one editable field (an explicit null on a date counts as a clear),
// a curation state within its enum, and a fact_type that is world/experience.
// The reference raises on the first violation only, and its error carries the
// whole request object as the offending input, so both are reproduced here.
func validateMemoryUpdate(body MemoryUpdateRequest, fields map[string]bool, input any) *httpx.ValidationFailure {
	failure := func(message string) *httpx.ValidationFailure {
		return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.ValueError([]string{"body"}, input, message),
		}}
	}
	hasDateClear := fields["occurred_start"] || fields["occurred_end"]
	if body.Text == nil && body.Context == nil && body.OccurredStart == nil &&
		body.OccurredEnd == nil && body.FactType == nil && body.Entities == nil && body.State == nil &&
		!hasDateClear {
		return failure("Provide at least one field to update.")
	}
	if body.State != nil && *body.State != "valid" && *body.State != "invalidated" {
		return failure("state must be 'valid' or 'invalidated'.")
	}
	if body.FactType != nil && *body.FactType != "world" && *body.FactType != "experience" {
		return failure("fact_type must be 'world' or 'experience'.")
	}
	return nil
}

// MemoryHistory returns the recorded changes of one memory unit as the bare
// array the reference emits.
func (h *Handlers) MemoryHistory(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	memoryID := strings.TrimSpace(r.PathValue("memory_id"))
	if !uuidAnyForm(memoryID) {
		httpx.WriteErrorFor(w, r, memoryIDInvalid(memoryID))
		return
	}
	unit, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, memoryLookupError(err, memoryID))
		return
	}
	// A memory outside the caller's read scope reads as not-found, so the answer
	// never confirms the id exists.
	if !storage.TagsVisibleUnderScope(unit.Tags, memoryops.ScopeFromRequest(r)) {
		httpx.WriteErrorFor(w, r, memoryUnitNotFound(memoryID))
		return
	}
	// Only observations carry a history; a source fact reports an empty array.
	if unit.FactType != storage.TypeObservation {
		httpx.WriteJSON(w, http.StatusOK, []MemoryHistoryEntry{})
		return
	}
	entries, err := h.Store.MemoryHistory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	facts, err := h.historySourceFacts(r.Context(), bankID, unit.SourceMemoryIDs, entries)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, renderMemoryHistory(unit.SourceMemoryIDs, entries, facts))
}

// historySourceFacts resolves every source fact the history view can name — the
// observation's current sources plus every id a change folded in — so the
// per-entry views below are pure lookups.
func (h *Handlers) historySourceFacts(ctx context.Context, bankID string, current []string, entries []storage.MemoryHistoryEntry) (map[string]SourceFactModel, error) {
	facts := map[string]SourceFactModel{}
	for _, id := range append(append([]string(nil), current...), historySourceIDs(entries)...) {
		if _, ok := facts[id]; ok {
			continue
		}
		fact, err := h.historySourceFact(ctx, bankID, id)
		if err != nil {
			return nil, err
		}
		facts[id] = fact
	}
	return facts, nil
}

// historySourceIDs lists the source ids the entries folded in.
func historySourceIDs(entries []storage.MemoryHistoryEntry) []string {
	ids := make([]string, 0)
	for _, entry := range entries {
		ids = append(ids, entry.NewSourceMemoryIDs...)
	}
	return ids
}

// historySourceFact renders one source fact, or a placeholder carrying only the
// id when the fact is gone — the shape the reference falls back to.
func (h *Handlers) historySourceFact(ctx context.Context, bankID, id string) (SourceFactModel, error) {
	unit, err := h.Store.GetMemory(ctx, bankID, id)
	if err != nil {
		if errors.Is(err, storage.ErrMemoryNotFound) {
			return SourceFactModel{ID: id}, nil
		}
		return SourceFactModel{}, err
	}
	text, factType := unit.Text, string(unit.FactType)
	model := SourceFactModel{ID: id, Text: &text, Type: &factType}
	// The reference reports an empty context as null, not as "".
	if unit.Context != "" {
		value := unit.Context
		model.Context = &value
	}
	return model, nil
}

// renderMemoryHistory rebuilds each change's cumulative source set by working
// backwards from the observation's current sources: sources are only ever
// accumulated, so the set held after a change is the set held at the next one
// minus the ids that change folded in. The reference walks the same way and
// emits the result oldest-first.
func renderMemoryHistory(current []string, entries []storage.MemoryHistoryEntry, facts map[string]SourceFactModel) []MemoryHistoryEntry {
	cumulative := append([]string(nil), current...)
	// Emitted newest-first during the walk, then flipped to ascending order.
	history := make([]MemoryHistoryEntry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		newIDs := make(map[string]bool, len(entry.NewSourceMemoryIDs))
		for _, id := range entry.NewSourceMemoryIDs {
			newIDs[id] = true
		}
		held := make([]SourceFactModel, 0, len(cumulative))
		for _, id := range cumulative {
			fact, ok := facts[id]
			if !ok {
				fact = SourceFactModel{ID: id}
			}
			fact.ID = id
			fact.IsNew = newIDs[id]
			held = append(held, fact)
		}
		history = append(history, historyEntryModel(entry, held))
		prior := make([]string, 0, len(cumulative))
		for _, id := range cumulative {
			if !newIDs[id] {
				prior = append(prior, id)
			}
		}
		cumulative = prior
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	return history
}

// historyEntryModel renders one change with its resolved source facts.
func historyEntryModel(entry storage.MemoryHistoryEntry, facts []SourceFactModel) MemoryHistoryEntry {
	previousTags := entry.PreviousTags
	if previousTags == nil {
		previousTags = []string{}
	}
	newIDs := entry.NewSourceMemoryIDs
	if newIDs == nil {
		newIDs = []string{}
	}
	changedAt := httpx.FormatTimestamp(entry.ChangedAt)
	return MemoryHistoryEntry{
		PreviousText:          entry.PreviousText,
		PreviousTags:          previousTags,
		PreviousOccurredStart: entry.PreviousOccurredStart,
		PreviousOccurredEnd:   entry.PreviousOccurredEnd,
		PreviousMentionedAt:   entry.PreviousMentionedAt,
		ChangedAt:             &changedAt,
		NewSourceMemoryIDs:    newIDs,
		SourceFacts:           facts,
	}
}

// memoryDetail renders the reference's memory-unit detail dict.
func (h *Handlers) memoryDetail(ctx context.Context, bankID string, unit storage.MemoryUnit) (map[string]any, error) {
	// Entity postings, resolved through the same inheritance the list view uses
	// so an observation with no postings of its own reports its sources'.
	names, err := h.Store.UnitEntityNames(ctx, bankID, []string{unit.ID})
	if err != nil {
		return nil, err
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
	var observationScopes json.RawMessage
	if len(unit.ObservationScopes) > 0 {
		observationScopes = unit.ObservationScopes
	}
	detail := map[string]any{
		"id": unit.ID, "text": unit.Text, "context": unit.Context, "date": date,
		"type": factTypeValue(unit.FactType), "mentioned_at": httpx.FormatTimestampPtr(unit.MentionedAt),
		"occurred_start": httpx.FormatTimestampPtr(unit.OccurredStart),
		"occurred_end":   httpx.FormatTimestampPtr(unit.OccurredEnd),
		"entities":       entities, "document_id": nullableString(unit.DocumentID),
		"chunk_id": nullableString(unit.ChunkID), "tags": tags, "metadata": metadata,
		"observation_scopes": observationScopes, "state": memoryState(unit),
		"invalidation_reason": unit.InvalidationReason,
		"invalidated_at":      httpx.FormatTimestampPtr(unit.InvalidatedAt),
		"edited_at":           httpx.FormatTimestampPtr(unit.EditedAt),
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
	if attachments, ok := h.Store.(storage.MemoryAttachmentStore); ok {
		byUnit, err := attachments.MemoryAttachments(ctx, bankID, []string{unit.ID})
		if err != nil {
			return nil, err
		}
		if records := byUnit[unit.ID]; len(records) > 0 {
			detail["attachments"] = memoryAttachmentItems(bankID, records)
		}
	}
	return detail, nil
}

// memoryAttachmentItems renders a memory's attachments in the reference's
// detail shape.
func memoryAttachmentItems(bankID string, records []storage.Attachment) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		var filename *string
		if record.Filename != "" {
			value := record.Filename
			filename = &value
		}
		out = append(out, map[string]any{
			"id": record.ShortID, "hash": record.AttachmentHash, "kind": record.Kind,
			"media_type": record.MediaType, "byte_size": record.ByteSize, "filename": filename,
			"url": "/v1/default/banks/" + bankID + "/attachments/" + record.ShortID,
		})
	}
	return out
}

// factTypeValue renders the unit's fact type for the detail dict.
func factTypeValue(factType storage.MemoryUnitType) any {
	if factType == "" {
		return nil
	}
	return string(factType)
}

// sourceMemories resolves an observation's source facts to summaries, newest
// mention first (nulls last), as the reference orders them. The rows come
// straight from the live table, so a source that exists only in the invalidation
// archive is dropped and a NULL text or context stays null.
func (h *Handlers) sourceMemories(ctx context.Context, bankID string, ids []string) ([]map[string]any, error) {
	reader, ok := h.Store.(storage.SourceMemoryReader)
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

// sourceMemoryType renders a source fact's type, which stays null when the live
// column carried none.
func sourceMemoryType(factType string) any {
	if factType == "" {
		return nil
	}
	return factType
}

// dryRunExtractRequest is the dry-run body. Everything below the content is a
// prompt-affecting override applied for this call only. Content stays raw so a
// content value the union does not accept is reported as the framework's
// structured 422 rather than failing the whole body decode.
type dryRunExtractRequest struct {
	Content                  json.RawMessage  `json:"content"`
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

// content decodes the raw content value into the canonical form the preview
// flattens and chunks. It is only called once dryRunContentFailure has accepted
// the value, so a decode failure here yields the zero content.
func (b dryRunExtractRequest) content() memoryops.Content {
	if len(b.Content) == 0 {
		return memoryops.Content{}
	}
	var input ContentInput
	if err := json.Unmarshal(b.Content, &input); err != nil {
		return memoryops.Content{}
	}
	return input.Content()
}

// DryRunExtract reports what a retain would extract, without storing anything.
func (h *Handlers) DryRunExtract(w http.ResponseWriter, r *http.Request) {
	if h.DryRunExtractDisabled {
		// The reference keeps the route registered and answers 404, naming the
		// key that re-enables it.
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound,
			"Dry-run extraction is disabled. Set HINDSIGHT_API_ENABLE_DRY_RUN_EXTRACT=true to re-enable."))
		return
	}
	// The reference neither requires nor creates the bank: an unknown bank is
	// answered with default extraction settings, so the path id is taken as
	// given and never probed.
	raw, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	httpx.RecordUnknownBodyFields(r, raw, dryRunExtractRequest{})
	var body dryRunExtractRequest
	if _, err := httpx.DecodeLax(raw, &body); err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	// The content value is validated the way the reference's request model does,
	// before any extraction runs: a blank value, a malformed block or a bad
	// payload is refused here rather than degrading into a quieter answer.
	if failure, badRequest := dryRunContentFailure(body.Content); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	} else if badRequest != "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, badRequest))
		return
	}
	contentValue := body.content()
	contentText := contentValue.Flatten()

	inline := contentValue.InlineAttachments()
	// An attachment-bearing request needs a model that reads images. The gate is
	// taken before any call so a deployment without one refuses up front rather
	// than extracting from the caption alone.
	if len(inline) > 0 {
		if gate, ok := h.Extraction.(interface{ VisionRefusal() error }); ok {
			if refusal := gate.VisionRefusal(); refusal != nil {
				httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindUnprocessable, refusal.Error()))
				return
			}
		}
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
	if body.EntityLabels != nil {
		// The override replaces the label vocabulary for this call, exactly as a
		// bank's own entity_labels would for a retain.
		if vocabulary, err := entitylabels.Parse(body.EntityLabels); err == nil {
			options.Vocabulary = vocabulary
		}
	}
	if body.EntitiesAllowFreeForm != nil {
		options.AllowFreeFormEntities = body.EntitiesAllowFreeForm
	}
	if body.LLMOutputLanguage != nil {
		options.OutputLanguage = *body.LLMOutputLanguage
	}
	if body.RetainExtractCausalLinks != nil {
		options.ExtractCausalLinks = body.RetainExtractCausalLinks
	} else if h.RetainExtractCausalLinks != nil {
		options.ExtractCausalLinks = h.RetainExtractCausalLinks
	}
	if body.Timestamp != nil {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*body.Timestamp)); err == nil {
			options.ReferenceTime = parsed
		}
	}

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
				// A refusal from the extractor's own attachment gate maps to the
				// reference's 422, not the generic 500. The up-front gate above
				// already refuses before this point; this keeps the mapping
				// correct if a call still carries attachments into the extractor.
				var vision *extraction.VisionNotSupportedError
				if errors.As(err, &vision) {
					httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindUnprocessable, vision.Detail))
					return
				}
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
	httpx.WriteJSON(w, http.StatusOK, DryRunExtractionResult{
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

// PromptPreview reports the prompt an operation would send for a bank, without
// running it. The operation is validated before the bank is resolved, matching
// the reference's request-model validation running ahead of the handler.
func (h *Handlers) PromptPreview(w http.ResponseWriter, r *http.Request) {
	raw, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	if len(raw) == 0 {
		writeRequiredBodyFailure(w, r, httpx.ErrEmptyBody)
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, raw))
		return
	}
	operation, input, ok := previewOperation(fields)
	if !ok {
		writeLiteralOperationFailure(w, input)
		return
	}
	// The reference's preview route is the one bank-scoped route that does NOT
	// catch OperationValidationError: a missing bank escapes to the generic
	// handler, which answers 500 with the wrapped text rather than the route's
	// 404. The bank-existence guard below is requireBank's, with the reference's
	// uncaught shape.
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		writeUncaughtBankValidationError(w, r, "")
		return
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		writeUncaughtBankValidationError(w, r, bankID)
		return
	}
	response := h.renderPromptPreview(r, bankID, operation, previewStrategyValue(fields))
	applyPreviewEditability(&response)
	httpx.WriteJSON(w, http.StatusOK, response)
}

// writeUncaughtBankValidationError answers a missing bank the way a route that
// does NOT catch OperationValidationError does: the generic handler maps the
// escaped failure to 500 with the wrapped text as detail.
func writeUncaughtBankValidationError(w http.ResponseWriter, r *http.Request, bankID string) {
	err := &httpx.OperationValidationError{Reason: httpx.BankNotFoundDetail(bankID), Status: httpx.KindNotFound}
	httpx.WriteErrorFor(w, r, err.Uncaught())
}

// directivesFor returns the bank's standing directives, so the reflect preview
// shows the section the real run would add. Each directive keeps its name, so
// the preview renders the same bolded rule the real prompt does.
func (h *Handlers) directivesFor(ctx context.Context, bankID string) []reflect.Directive {
	if strings.TrimSpace(bankID) == "" {
		return nil
	}
	directives, err := h.Store.ListDirectives(ctx, bankID)
	if err != nil {
		return nil
	}
	out := make([]reflect.Directive, 0, len(directives))
	for _, directive := range directives {
		if strings.TrimSpace(directive.Content) != "" {
			out = append(out, reflect.Directive{Name: directive.Name, Content: directive.Content})
		}
	}
	return out
}
