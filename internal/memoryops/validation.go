package memoryops

import (
	"strconv"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Pagination bounds validated with lower and upper limits.
const (
	MinLimit  = 1
	MaxLimit  = 1000
	MinOffset = 0
)

// DefaultOperationLimit is the page size applied when a list request omits one.
const DefaultOperationLimit = 50

// ValidateRetain checks the retain request fields, including the batch gate,
// duplicate document identifiers, multi-strategy plus an operation identifier,
// and identifier shape.
func ValidateRetain(req RetainRequest, batchEnabled bool) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	if req.Items == nil {
		items = append(items, httpx.Missing([]string{"body", "items"}, map[string]any{}))
	} else if len(req.Items) < MinWriteItemsCount {
		items = append(items, httpx.ValueError([]string{"body", "items"}, []any{},
			"items must contain at least one entry"))
	}
	if batchEnabled && !req.Async {
		items = append(items, httpx.ValueError([]string{"body", "async"}, req.Async,
			"batch write mode requires asynchronous processing; set async to true"))
	}
	for index, item := range req.Items {
		if item.Content.Flatten() == "" && !item.Content.HasImage() {
			items = append(items, httpx.Missing([]string{"body", "items", strconv.Itoa(index), "content"},
				map[string]any{}))
		}
		items = append(items, itemFieldErrors(index, item)...)
	}
	if req.OperationID != "" && !IsValidUUIDShape(req.OperationID) {
		items = append(items, httpx.ValueError([]string{"body", "operation_id"}, req.OperationID,
			"operation_id must be a valid UUID"))
	}
	if req.OperationID != "" && strategyGroupCount(req.Items) > 1 {
		items = append(items, httpx.ValueError([]string{"body", "operation_id"}, req.OperationID,
			"operation_id cannot be combined with items that resolve to more than one strategy"))
	}
	// The queued path fans items out to parallel workers with no per-document
	// gate, so duplicate document ids there race; the synchronous path folds
	// shared-document items into one document sequentially and is safe, which is
	// what a client sending several turns for one document relies on.
	if req.Async {
		if dup := duplicateDocumentID(req.Items); dup != "" {
			items = append(items, httpx.ValueError([]string{"body", "items"}, dup,
				"document_id must be unique within a request"))
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}

// itemFieldErrors reports the per-item failures a decode-time custom validator
// raises, at the item's own field path so the item names the exact member the
// reference names. Both checks decode without failing so a whole body is still
// described field by field, the way the reference's own validators report.
func itemFieldErrors(index int, item RetainItem) []httpx.ValidationError {
	var items []httpx.ValidationError
	if item.Timestamp.Invalid {
		items = append(items, httpx.ValueError(
			[]string{"body", "items", strconv.Itoa(index), "timestamp"},
			item.Timestamp.InvalidInput, item.Timestamp.InvalidMessage))
	}
	if item.ObservationScopes.Invalid {
		items = append(items, httpx.ValueError(
			[]string{"body", "items", strconv.Itoa(index), "observation_scopes"},
			item.ObservationScopes.InvalidInput,
			"observation_scopes must be a mode name or a list of tag groups"))
	}
	return items
}

// blockedWriteFailure reports the unprocessable-entity failure for a write that
// carries inline image content against a write model that cannot read images.
//
// Failing before a byte is written is deliberate: extracting from the caption
// and quietly ignoring the image would store a document that looks retained
// while the information the caller cared about is gone. A vision-capable write
// model accepts the images and returns nil.
func blockedWriteFailure(req RetainRequest, visionEnabled bool) *httpx.ValidationFailure {
	if visionEnabled {
		return nil
	}
	carriesImage := false
	for _, item := range req.Items {
		if item.Content.HasImage() {
			carriesImage = true
			break
		}
	}
	if !carriesImage {
		return nil
	}
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.ValueError([]string{"body", "items", "content"}, nil,
			"image content is not supported by the configured write model"),
	}}
}

// strategyGroupCount counts the distinct named strategies across items; an
// empty strategy counts as one unnamed group.
func strategyGroupCount(items []RetainItem) int {
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.Strategy] = true
	}
	return len(seen)
}

// duplicateDocumentID returns the first repeated non-empty document identifier.
func duplicateDocumentID(items []RetainItem) string {
	seen := map[string]bool{}
	for _, item := range items {
		id := item.DocumentID
		if id == "" {
			continue
		}
		if seen[id] {
			return id
		}
		seen[id] = true
	}
	return ""
}

// ValidateOperationList checks the listing pagination bounds.
func ValidateOperationList(limit, offset int) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	if limit != 0 && (limit < MinLimit || limit > MaxLimit) {
		if limit < MinLimit {
			items = append(items, httpx.GreaterThanEqual([]string{"query", "limit"}, limit, MinLimit))
		} else {
			items = append(items, httpx.LessThanEqual([]string{"query", "limit"}, limit, MaxLimit))
		}
	}
	if offset < MinOffset {
		items = append(items, httpx.GreaterThanEqual([]string{"query", "offset"}, offset, MinOffset))
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}

// ValidateRecall checks the recall request fields: the required query, the
// fact-type selection, the budget level, the tag-scoping form, the tag-matching
// mode and the pagination bounds.
func ValidateRecall(req RecallRequest) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	if !hasWordCharacter(req.Query) {
		items = append(items, httpx.ValueError([]string{"body", "query"}, req.Query,
			"query must contain at least one word character after normalization"))
	}
	if req.Budget != "" && !containsToken(storageBudgetLevels(), req.Budget) {
		items = append(items, httpx.Enum([]string{"body", "budget"}, req.Budget, storageBudgetLevels()))
	}
	if req.TagsMatch != "" && !containsToken(storageTagMatchModes(), req.TagsMatch) {
		items = append(items, httpx.Literal([]string{"body", "tags_match"}, req.TagsMatch, storageTagMatchModes()))
	}
	if unsupported := unsupportedTypes(req.Types); len(unsupported) > 0 {
		items = append(items, httpx.ValueError([]string{"body", "types"}, unsupported,
			"unsupported fact type; accepted types are "+joinTokens(acceptedFactTypes)))
	}
	if len(req.Tags) > 0 && len(req.TagGroups.Groups) > 0 {
		items = append(items, httpx.ValueError([]string{"body"}, nil,
			"'tags' and 'tag_groups' are mutually exclusive. Use 'tag_groups' for compound filtering."))
	}
	if invertedTemporalWindow(req.TemporalWindow) {
		items = append(items, httpx.ValueError([]string{"body", "temporal_window"}, nil,
			"temporal_window end must not be earlier than start"))
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}

// invertedTemporalWindow reports whether a window's upper bound is earlier than
// its lower bound. An unparseable bound is not treated as inverted here; it is
// rejected on the bad-request path.
func invertedTemporalWindow(window *TemporalWindowRequest) bool {
	if window == nil {
		return false
	}
	start, errStart := parseISODate(window.Start)
	end, errEnd := parseISODate(window.End)
	if errStart != nil || errEnd != nil {
		return false
	}
	return end.Before(start)
}

// tagGroupFailure reports a tag-group filter that would constrain nothing.
func tagGroupFailure() *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.ValueError([]string{"body", "tag_groups"}, nil, ErrMalformedTagGroup.Error()),
	}}
}

// hasWordCharacter reports whether a query carries at least one searchable word
// character.
func hasWordCharacter(query string) bool {
	for _, r := range query {
		if isWordCharacter(r) {
			return true
		}
	}
	return false
}

// isWordCharacter reports whether a rune is a letter, digit or underscore.
func isWordCharacter(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r > 127
}

// storageBudgetLevels returns the accepted budget levels.
func storageBudgetLevels() []string { return storage.RecallBudgetLevels() }

// storageTagMatchModes returns the accepted tag-matching modes.
func storageTagMatchModes() []string { return storage.RecallTagMatchModes() }

// containsToken reports whether a value belongs to a token set.
func containsToken(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// joinTokens renders a token set for a validation message.
func joinTokens(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}
