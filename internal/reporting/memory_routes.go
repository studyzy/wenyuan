package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/extraction"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/reflect"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ListMemories returns one page of a bank's memory units.
func (h *Handlers) ListMemories(w http.ResponseWriter, r *http.Request) {
	// The framework validates the typed query members before the handler runs,
	// and the handler's own first act is the bank lookup
	// (engine/memory_engine.py:13216-13217). The store's validations of `state`,
	// `entity_id`, `consolidation_state` and the time window are only reached
	// after it, so an unknown bank answers 404 ahead of every store-side
	// complaint — while the two framework-side members (`tags_match`,
	// `time_field`, both `Literal`s), the two date parsers (evaluated with the
	// handler's arguments, api/http.py:5892-5893) and the two bound integers
	// answer first.
	query := httpx.QueryParamsOf(r)
	if _, present := query["tags_match"]; present {
		tagsMatch := queryLast(r, "tags_match")
		if !containsString(tagsMatchModes, tagsMatch) {
			writeQueryEnumFailure(w, r, "tags_match", tagsMatch, tagsMatchModes)
			return
		}
	}
	if _, present := query["time_field"]; present {
		timeField := queryLast(r, "time_field")
		if !containsString(memoryTimeFields, timeField) {
			writeQueryEnumFailure(w, r, "time_field", timeField, memoryTimeFields)
			return
		}
	}
	start, ok := lastDateParam(w, r, "start_date")
	if !ok {
		return
	}
	end, ok := lastDateParam(w, r, "end_date")
	if !ok {
		return
	}
	limit, ok := nonNegativeIntParam(w, r, "limit", 100, memoryListLimitPlaceholder)
	if !ok {
		return
	}
	offset, ok := nonNegativeIntParam(w, r, "offset", 0, memoryListOffsetPlaceholder)
	if !ok {
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	if _, present := query["state"]; present {
		state := queryLast(r, "state")
		if state != "valid" && state != "invalidated" {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
				fmt.Sprintf("Invalid state '%s': expected 'valid' or 'invalidated'.", state)))
			return
		}
	}
	rawConsolidationState := queryLast(r, "consolidation_state")
	consolidationState := strings.ToLower(rawConsolidationState)
	if consolidationState != "" && !containsString(consolidationStates, consolidationState) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid consolidation_state '%s': expected 'failed', 'pending', or 'done'.", rawConsolidationState)))
		return
	}
	entityID := queryLast(r, "entity_id")
	if _, present := query["entity_id"]; present && !uuidAnyForm(entityID) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid entity_id: '%s' is not a valid UUID", entityID)))
		return
	}
	// The window is half-open [start, end): an inverted window is a bad request,
	// not a silently empty page.
	if start != nil && end != nil && !end.After(*start) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"Invalid time window: end_date must be after start_date."))
		return
	}
	tagsMatch := queryLast(r, "tags_match")
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAny
	}
	state := queryLast(r, "state")
	timeField := queryLast(r, "time_field")
	// The caller's resolved read scope is AND-ed into the tag filter, so a
	// scoped reader pages over the memories it may read and the total agrees.
	tagGroups, tags, tagsMatch := memoryops.ApplyMemoryTagScope(r, httpx.QueryValues(r, "tags"), tagsMatch)
	// The filters are bound as the caller sent them: the reference hands the
	// request model's values to the store, which treats only "" as absent, so a
	// whitespace-padded value is a filter that matches nothing rather than one
	// that was meant to be trimmed away.
	page, err := h.Store.ListMemories(r.Context(), storage.MemoryQuery{
		BankID:             bankID,
		Type:               queryLast(r, "type"),
		Q:                  queryLast(r, "q"),
		DocumentID:         queryLast(r, "document_id"),
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
	// answers for the whole page. The listing shows a unit's own postings: an
	// observation, which carries none, reports no entities here even though its
	// detail view inherits its sources'.
	var entityNames map[string][]string
	if direct, ok := h.Store.(directEntityStore); ok {
		entityNames, err = direct.DirectUnitEntityNames(r.Context(), bankID, unitIDs)
	} else {
		entityNames, err = h.Store.UnitEntityNames(r.Context(), bankID, unitIDs)
	}
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

// directEntityStore is implemented by a store that reports a unit's own entity
// postings without the observation → source inheritance the detail read applies,
// which is what the listing shows.
type directEntityStore interface {
	DirectUnitEntityNames(ctx context.Context, bankID string, unitIDs []string) (map[string][]string, error)
}

// tagsMatchModes are the accepted tags_match values.
var tagsMatchModes = []string{"any", "all", "any_strict", "all_strict", "exact"}

// memoryTimeFields are the accepted time_field values.
var memoryTimeFields = []string{"created_at", "updated_at", "mentioned_at", "occurred_start", "occurred_end"}

// consolidationStates are the accepted consolidation_state values.
var consolidationStates = []string{"failed", "pending", "done"}

// The page bounds' placeholder numbers in the list query. The reference binds
// bank_id, then limit, then offset (engine/memories/pg/curation.py:284-292), so
// a value only the driver can reject is named by the position it occupies —
// `$2` for limit, `$3` for offset.
const (
	memoryListLimitPlaceholder  = 2
	memoryListOffsetPlaceholder = 3
)

// nonNegativeIntParam reads an optional integer query parameter with a default,
// rejecting a malformed or negative value with the framework's 422 body.
func nonNegativeIntParam(w http.ResponseWriter, r *http.Request, name string, def, placeholder int) (int, bool) {
	values, present := httpx.QueryPresent(r, name)
	if !present || len(values) == 0 {
		return def, true
	}
	// The reference binds the LAST occurrence of a repeated scalar parameter.
	raw := values[len(values)-1]
	// The reference's model declares an unbounded Python int and the coercion is
	// its own (a zero fraction and single underscores allowed), so the shared
	// helper reads the value and this route only applies its bound.
	parsed, ok := frameworkIntFromString(raw)
	if !ok {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.TypeMismatch("int_parsing", []string{"query", name}, raw),
		}})
		return 0, false
	}
	if parsed.Sign() < 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.GreaterThanEqual([]string{"query", name}, raw, 0),
		}})
		return 0, false
	}
	if !parsed.IsInt64() {
		// The framework's model keeps the value as an unbounded Python int, so
		// it validates; only the driver's binding narrows it, and the route
		// answers 500 with that driver error rather than a 422.
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, fmt.Sprintf(
			"invalid input for query argument $%d: %s (value out of int64 range)", placeholder, raw)))
		return 0, false
	}
	value := int(parsed.Int64())
	if value < 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.GreaterThanEqual([]string{"query", name}, raw, 0),
		}})
		return 0, false
	}
	return value, true
}

// queryLast reads a scalar query parameter and returns its final occurrence —
// the value the reference's request model binds when a client repeats a scalar
// parameter — recording the read the way httpx.Query does.
func queryLast(r *http.Request, name string) string {
	values, present := httpx.QueryPresent(r, name)
	if !present || len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// lastDateParam is parseDateParam with the reference's repeated-scalar binding:
// a repeated start_date/end_date binds its final value. An absent parameter is
// nothing; a present-but-unparseable one is the reference's bad request.
func lastDateParam(w http.ResponseWriter, r *http.Request, key string) (*time.Time, bool) {
	if _, present := httpx.QueryParamsOf(r)[key]; !present {
		return nil, true
	}
	raw := queryLast(r, key)
	parsed, err := pythonISODateTime(raw)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Invalid %s: '%s' is not an ISO-8601 datetime.", key, raw)))
		return nil, false
	}
	utc := parsed.UTC()
	return &utc, true
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
// This is the validity test only; memoryUnitID is the exact port of CPython's
// rule and additionally produces the canonical spelling for the store.
func uuidAnyForm(value string) bool {
	s := strings.ToLower(value)
	s = strings.ReplaceAll(s, "urn:", "")
	s = strings.ReplaceAll(s, "uuid:", "")
	s = strings.Trim(s, "{}")
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

// memoryUnitID normalizes a path identifier the way the reference's by-id
// memory methods do before they touch the store — `str(uuid.UUID(memory_id))`
// in engine/memory_engine.py (`get_memory_unit`, `update_memory_unit`,
// `get_observation_history`). A value that is not a UUID is the reference's 400
// "Invalid memory_id: …", naming the identifier exactly as the client sent it.
//
// The acceptance rule is CPython's, which is what the reference runs, and it is
// looser than a strict 8-4-4-4-12 parse in ways a client can hit:
//
//	hex = hex.replace('urn:', '').replace('uuid:', '')
//	hex = hex.strip('{}').replace('-', '')
//
// So the two prefixes are removed wherever they appear and in any case, every
// leading and trailing brace is stripped (not just one pair), and hyphens are
// dropped wherever they are — `urn:uuid:`- and brace-wrapped forms, an
// all-hex 32-character form and a mis-grouped hyphenated form all reach the
// query as the same canonical UUID. Whitespace is NOT trimmed: it is part of
// the identifier and fails the check, with the untrimmed value echoed back.
// Handing one of these to the driver instead answers a 500, which the reference
// never does on these routes.
func memoryUnitID(memoryID string) (string, *httpx.HTTPError) {
	s := strings.ReplaceAll(memoryID, "urn:", "")
	s = strings.ReplaceAll(s, "uuid:", "")
	s = strings.Trim(s, "{}")
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return "", memoryIDInvalid(memoryID)
	}
	var canonical strings.Builder
	canonical.Grow(36)
	for i, r := range s {
		if !isHexRune(r) {
			return "", memoryIDInvalid(memoryID)
		}
		if i == 8 || i == 12 || i == 16 || i == 20 {
			canonical.WriteByte('-')
		}
		canonical.WriteRune(unicode.ToLower(r))
	}
	return canonical.String(), nil
}

// GetMemory returns one memory unit's detail.
func (h *Handlers) GetMemory(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	sentID := r.PathValue("memory_id")
	// The reference rejects a malformed id before touching the store; a missing
	// bank is not checked at all, so an unknown bank reads as a missing unit.
	memoryID, invalid := memoryUnitID(sentID)
	if invalid != nil {
		httpx.WriteErrorFor(w, r, invalid)
		return
	}
	unit, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, memoryLookupError(err, sentID))
		return
	}
	// A memory outside the caller's read scope reads as not-found, so the answer
	// never confirms the id exists.
	if !storage.TagsVisibleUnderScope(unit.Tags, memoryops.ScopeFromRequest(r)) {
		httpx.WriteErrorFor(w, r, memoryUnitNotFound(sentID))
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

// memoryIDDriverFailure is the 500 the reference answers when an identifier it
// never validates reaches PostgreSQL as a uuid. Every route that normalizes the
// id first (`get_memory_unit`, `update_memory_unit`, `get_observation_history`)
// turns a malformed one into a 400 reading "Invalid memory_id: …"; the
// clear-observations route does not normalize it, so the driver's own uuid
// parse error escapes to the route's catch-all. The reference runs asyncpg
// there, whose python uuid parse reports "badly formed hexadecimal UUID string".
func memoryIDDriverFailure() *httpx.HTTPError {
	return httpx.NewHTTPError(httpx.KindInternal, "badly formed hexadecimal UUID string")
}

// ClearMemoryObservations answers DELETE .../memories/{memory_id}/observations.
//
// It is the reference handler with the one difference that route has from every
// other by-id memory read: the id is not normalized before use, so a malformed
// one reaches the driver and the route's catch-all answers 500 with the
// driver's uuid error instead of the named 400 the normalizing routes answer.
// The driver call is the last step, after the scope gate — a scoped caller is
// answered by that gate about the memory, whatever the id's shape.
func (h *Handlers) ClearMemoryObservations(w http.ResponseWriter, r *http.Request) {
	memoryID := strings.TrimSpace(r.PathValue("memory_id"))
	if memoryID != "" && !uuidAnyForm(memoryID) && !memoryScopeInForce(r) {
		httpx.WriteErrorFor(w, r, memoryIDDriverFailure())
		return
	}
	h.clearObservationsAndReconsolidate(w, r)
}

// clearObservationsAndReconsolidate is the body of ClearMemoryObservations plus
// the follow-up the reference's engine performs: when the clear actually removed
// observations and the bank consolidates automatically, one consolidation is
// submitted so the memory produces fresh observations on the next run
// (engine/memory_engine.py:11855-11859 → :11879-11882). A clear that removed
// nothing submits nothing, and a bank with automatic consolidation off never
// submits.
func (h *Handlers) clearObservationsAndReconsolidate(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	memoryID := strings.TrimSpace(r.PathValue("memory_id"))
	if memoryID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "memory not found"))
		return
	}
	if !h.requireWritableMemory(w, r, bankID, memoryID) {
		return
	}
	removed, err := h.Store.DeleteObservations(r.Context(), bankID, memoryID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	if removed > 0 && h.autoConsolidationEnabled(r, bankID) {
		if _, _, err := h.Store.EnqueueConsolidation(r.Context(), bankID); err != nil {
			// The clear is already durable; consolidation is a follow-up the
			// reconcile sweep picks up, so a failed enqueue is not the clear's
			// failure.
			slog.Default().Warn("could not enqueue consolidation after clearing observations",
				"bank_id", bankID, "error", err)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, ClearMemoryObservationsResponse{DeletedCount: removed})
}

// autoConsolidationEnabled reports the bank's effective enable_auto_consolidation:
// the bank's own override, else the caller's tenant override, else the
// deployment default — the same layering the reference's config resolver applies.
func (h *Handlers) autoConsolidationEnabled(r *http.Request, bankID string) bool {
	bankConfig, err := h.Store.BankConfig(r.Context(), bankID)
	if err != nil && !errors.Is(err, storage.ErrBankNotFound) {
		return false
	}
	resolved := config.ResolveLayers(bankConfig, httpx.TenantOverridesFrom(r), h.ProcessDefaults)
	enabled, ok := resolved["enable_auto_consolidation"].(bool)
	return ok && enabled
}

// memoryScopeInForce reports whether a tenant scope narrows this request, in
// which case the write-scope gate answers before any id reaches the driver. It
// mirrors requireWritableMemory's own "no scope, no gate" test.
func memoryScopeInForce(r *http.Request) bool {
	readScope := memoryops.ScopeFromRequest(r)
	_, confined := memoryops.WriteScopeFromRequest(r)
	return len(readScope) > 0 || confined
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
	sentID := r.PathValue("memory_id")
	var body MemoryUpdateRequest
	raw, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	// The framework reads the body before it instantiates the model: an absent
	// body is the `missing` item for the required body field, and a body the
	// model rejects is the parser's or the field's own item — the decode
	// failure names the value, so it is rendered against the bytes sent.
	if len(raw) == 0 {
		httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(httpx.ErrEmptyBody, raw))
		return
	}
	if _, err := httpx.DecodeLax(raw, &body); err != nil {
		// A body carrying a numeric literal that overflows to infinity is not
		// one the framework's model ever sees: CPython parses it as `inf`, then
		// fails to serialize its own error, so the reference answers a bare 500
		// (api/http.py:6078-6132 escapes into the server's handler). Rendering
		// the Go decoder's message instead would name the model field, which is
		// not what the client would have received.
		if httpx.JSONDecodeFailure(raw) {
			unhandled := httpx.NewHTTPError(httpx.KindInternal, "")
			unhandled.Plain = true
			httpx.WriteErrorFor(w, r, unhandled)
			return
		}
		httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, raw))
		return
	}
	httpx.RecordUnknownBodyFields(r, raw, &body)
	fields := bodyFieldPresence(raw)
	if failure := validateMemoryUpdate(body, fields, bodyInput(raw)); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	memoryID, invalid := memoryUnitID(sentID)
	if invalid != nil {
		httpx.WriteErrorFor(w, r, invalid)
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
		httpx.WriteErrorFor(w, r, memoryLookupError(err, sentID))
		return
	}
	if existing.FactType != storage.TypeWorld && existing.FactType != storage.TypeExperience {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			fmt.Sprintf("Memory '%s' is a %s; only world/experience facts can be curated. "+
				"Observations are derived and regenerate from their sources.", sentID, existing.FactType)))
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
			fmt.Sprintf("Memory '%s' not found", sentID)))
		return
	}
	if writeTags, confined := memoryops.WriteScopeFromRequest(r); !writableUnderScope(existing.Tags, writeTags, confined) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindForbidden,
			fmt.Sprintf("You can't write memory '%s' tagged %s",
				sentID, strings.Join(unwritableTags(existing.Tags, writeTags), ", "))))
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
	sentID := r.PathValue("memory_id")
	// Both the reference's `get_observation_history` and the engine's own
	// `get_memory_unit` alias the fact type first and the unit read second, and
	// only then reach for the history rows (engine/memory_engine.py:13363-13460,
	// 13247-13362). A unit that has left the live table for the archive is a
	// miss there — the archive read is the detail route's, not this one's — so
	// its history answers 404 even though the unit still exists.
	memoryID, invalid := memoryUnitID(sentID)
	if invalid != nil {
		httpx.WriteErrorFor(w, r, invalid)
		return
	}
	unit, err := h.Store.GetMemory(r.Context(), bankID, memoryID)
	if err != nil || unit.InvalidatedAt != nil {
		var failure *httpx.HTTPError
		if err == nil || errors.Is(err, storage.ErrMemoryNotFound) {
			failure = memoryUnitNotFound(sentID)
		} else {
			failure = asHTTPError(err)
		}
		httpx.WriteErrorFor(w, r, failure)
		return
	}
	// A memory outside the caller's read scope reads as not-found, so the answer
	// never confirms the id exists.
	if !storage.TagsVisibleUnderScope(unit.Tags, memoryops.ScopeFromRequest(r)) {
		httpx.WriteErrorFor(w, r, memoryUnitNotFound(sentID))
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
	Timestamp                json.RawMessage  `json:"timestamp"`
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

// dryRunRetainConfig resolves the retain configuration a dry run extracts
// under: the bank's stored overrides layered over the tenant and process
// layers, with a named strategy — or the bank's own retain_default_strategy
// when none is named — applied on top. It is the resolution a retain performs,
// so a dry run previews the configuration the bank would actually use.
func (h *Handlers) dryRunRetainConfig(r *http.Request, bankID, strategy string) map[string]any {
	config := h.resolvePreviewConfig(r, bankID)
	if strategy == "" {
		strategy, _ = config["retain_default_strategy"].(string)
	}
	return applyRetainStrategy(config, strategy)
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
	// given — it selects the config to resolve, and a bank with none configured
	// (including one that does not exist) resolves the process defaults.
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	raw, err := readOptionalBody(r)
	if err != nil {
		writeRequiredBodyFailure(w, r, err)
		return
	}
	httpx.RecordUnknownBodyFields(r, raw, dryRunExtractRequest{})
	var body dryRunExtractRequest
	if decoded, err := httpx.DecodeLax(raw, &body); err != nil {
		// The decoder reports the value it refused as the item's input, so its
		// own rendering is used rather than one rebuilt from the error alone.
		httpx.WriteValidationFailure(w, httpx.BodyDecodeFailure(err, decoded))
		return
	}
	// The content value is validated the way the reference's request model does,
	// before any extraction runs: a blank value, a malformed block or a bad
	// payload is refused here rather than degrading into a quieter answer.
	if failure, badRequest := dryRunContentFailure(body.Content, decodedRequestBody(raw)); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	} else if badRequest != "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, badRequest))
		return
	}
	// The request model declares entity_labels as a list of LabelGroups, so the
	// framework validates it in the same pass as the content and refuses an
	// invalid one before the handler runs. An empty `[]` is accepted and an
	// absent member is absent, so only a supplied, non-empty list can fail.
	if len(body.EntityLabels) > 0 {
		if failure := dryRunEntityLabelFailure(body.EntityLabels); failure != nil {
			httpx.WriteValidationFailure(w, failure)
			return
		}
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

	// The bank's effective retain configuration is resolved here, the way a
	// retain resolves it: the request's named strategy (or the bank's default)
	// overlaid on the bank's stored config over the tenant and process layers.
	// Every prompt-affecting setting the body does not override is taken from
	// it, so a dry run against a configured bank previews what the bank sends.
	strategy := ""
	if body.Strategy != nil {
		strategy = *body.Strategy
	}
	resolved := h.dryRunRetainConfig(r, bankID, strategy)

	// The mode is the reference's resolution order: the resolved config's, then
	// a deployment with no model forced to chunks, then the body's override.
	mode := previewString(resolved, "retain_extraction_mode")
	if h.Extraction == nil {
		mode = "chunks"
	}
	if body.RetainExtractionMode != nil {
		mode = *body.RetainExtractionMode
	}

	options := storage.FactExtractionOptions{
		Mode:                   mode,
		Mission:                previewString(resolved, "retain_mission"),
		Instructions:           previewString(resolved, "retain_custom_instructions"),
		Strategy:               strategy,
		OptionalFactDimensions: h.RetainOptionalFactDimensions,
	}
	if body.RetainMission != nil {
		options.Mission = *body.RetainMission
	}
	if body.RetainCustomInstructions != nil {
		options.Instructions = *body.RetainCustomInstructions
	}
	if body.EntityLabels != nil {
		// The override replaces the label vocabulary for this call, exactly as a
		// bank's own entity_labels would for a retain.
		if vocabulary, err := entitylabels.Parse(body.EntityLabels); err == nil {
			options.Vocabulary = vocabulary
		}
	} else if vocabulary, err := entitylabels.Parse(resolved["entity_labels"]); err == nil {
		options.Vocabulary = vocabulary
	}
	if body.EntitiesAllowFreeForm != nil {
		options.AllowFreeFormEntities = body.EntitiesAllowFreeForm
	} else if value, ok := resolved["entities_allow_free_form"].(bool); ok {
		options.AllowFreeFormEntities = &value
	}
	if body.LLMOutputLanguage != nil {
		options.OutputLanguage = *body.LLMOutputLanguage
	}
	if body.RetainExtractCausalLinks != nil {
		options.ExtractCausalLinks = body.RetainExtractCausalLinks
	} else if h.RetainExtractCausalLinks != nil {
		options.ExtractCausalLinks = h.RetainExtractCausalLinks
	}
	if value, present, failure := dryRunTimestamp(body.Timestamp); failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	} else if present {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			options.ReferenceTime = parsed
		}
	}

	byBlock := make(map[int]memoryops.InlineAttachment, len(inline))
	for _, attachment := range inline {
		byBlock[attachment.BlockIndex] = attachment
	}

	chunkSize, _ := previewInt(resolved["retain_chunk_size"])
	structuredChunkSize, _ := previewInt(resolved["retain_structured_chunk_size"])
	if body.RetainChunkSize != nil {
		chunkSize = *body.RetainChunkSize
	}
	segments := storage.SegmentWith(contentText, storage.SegmentOptions{
		ChunkSize: chunkSize, StructuredChunkSize: structuredChunkSize,
	})
	segmentOccurrences := occurrencesBySegment(segments, dryRunOccurrences(inline))

	// Chunks mode stores each chunk verbatim and never asks the model, so the
	// preview reports one fact per chunk rather than extracted ones. The check is
	// the reference's own (an exact "chunks"), and a deployment with no model
	// resolves to that mode above.
	chunksMode := mode == "chunks"

	facts := make([]ExtractedFactModel, 0)
	chunks := make([]ExtractionChunk, 0, len(segments))
	usage := storage.TokenUsage{}
	for index, segment := range segments {
		occurrences := segmentOccurrences[index]
		result := storage.DryRunExtraction{}
		if chunksMode {
			result.Facts = []storage.ExtractedFact{{Text: segment}}
		} else if h.Extraction != nil {
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
			usage = addTokenUsage(usage, result.Usage)
		}
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
	// A JSON `null` body is the same failure as no body at all: the reference's
	// framework answers a required `body: Model` with the `missing` item whether
	// the client sent nothing or sent `null`, and it does so before the handler
	// runs.
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		writeRequiredBodyFailure(w, r, httpx.ErrEmptyBody)
		return
	}
	// The reference declares this route's body as a pydantic model, so a
	// non-object body is the framework's model_attributes_type item rather than
	// a plain dict_type the Go decoder would name.
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		httpx.WriteValidationFailure(w, httpx.ModelAttributesFailure(raw))
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
	// The model refuses a member of the wrong type before the route runs at all,
	// so a bad `strategy` is a 422 whatever bank the path names.
	if failure := previewStrategyFailure(fields); failure != nil {
		httpx.WriteValidationFailure(w, failure)
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
