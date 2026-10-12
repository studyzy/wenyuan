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
	if len(req.Items) < MinWriteItemsCount {
		items = append(items, httpx.ValidationError{
			Loc:  []string{"body", "items"},
			Msg:  "items must contain at least one entry",
			Type: "value_error.missing",
		})
	}
	if batchEnabled && !req.Async {
		items = append(items, httpx.ValidationError{
			Loc:  []string{"body", "async"},
			Msg:  "batch write mode requires asynchronous processing; set async to true",
			Type: "value_error.mode",
		})
	}
	for index, item := range req.Items {
		if item.Content.Flatten() == "" && !item.Content.HasImage() {
			items = append(items, httpx.ValidationError{
				Loc:  []string{"body", "items", strconv.Itoa(index), "content"},
				Msg:  "content is required",
				Type: "value_error.missing",
			})
		}
	}
	if req.OperationID != "" && !IsValidUUIDShape(req.OperationID) {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "operation_id"},
			Msg:   "operation_id must be a valid identifier",
			Type:  "value_error.identifier",
			Input: req.OperationID,
		})
	}
	if req.OperationID != "" && strategyGroupCount(req.Items) > 1 {
		items = append(items, httpx.ValidationError{
			Loc:  []string{"body", "operation_id"},
			Msg:  "operation_id cannot be combined with items that resolve to more than one strategy",
			Type: "value_error.mode",
		})
	}
	if dup := duplicateDocumentID(req.Items); dup != "" {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "items"},
			Msg:   "document_id must be unique within a request",
			Type:  "value_error.duplicate",
			Input: dup,
		})
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}

// blockedWriteFailure reports the unprocessable-entity failure for a write whose
// items all carry image content against a non-vision write model.
func blockedWriteFailure(req RetainRequest) *httpx.ValidationFailure {
	if len(req.Items) == 0 {
		return nil
	}
	allImages := true
	for _, item := range req.Items {
		if !item.Content.HasImage() {
			allImages = false
			break
		}
	}
	if !allImages {
		return nil
	}
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
		Loc:  []string{"body", "items", "content"},
		Msg:  "image content is not supported by the configured write model",
		Type: "value_error.unprocessable",
	}}}
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

// ValidateOperationID rejects an operation identifier that is not a valid
// identifier shape.
func ValidateOperationID(opID string) *httpx.ValidationFailure {
	if IsValidUUIDShape(opID) {
		return nil
	}
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
		Loc:   []string{"path", "operation_id"},
		Msg:   "operation_id must be a valid identifier",
		Type:  "value_error.identifier",
		Input: opID,
	}}}
}

// ValidateOperationList checks the listing pagination bounds.
func ValidateOperationList(limit, offset int) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	if limit != 0 && (limit < MinLimit || limit > MaxLimit) {
		items = append(items, httpx.ValidationError{
			Loc: []string{"query", "limit"}, Msg: "limit must be between 1 and 1000",
			Type: "value_error.range", Input: limit,
		})
	}
	if offset < MinOffset {
		items = append(items, httpx.ValidationError{
			Loc: []string{"query", "offset"}, Msg: "offset must not be negative",
			Type: "value_error.range", Input: offset,
		})
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
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "query"},
			Msg:   "query must contain at least one word character",
			Type:  "value_error.missing",
			Input: req.Query,
		})
	}
	if req.Budget != "" && !containsToken(storageBudgetLevels(), req.Budget) {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "budget"},
			Msg:   "budget must be one of low, mid, high",
			Type:  "value_error.enum",
			Input: req.Budget,
		})
	}
	if req.TagsMatch != "" && !containsToken(storageTagMatchModes(), req.TagsMatch) {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "tags_match"},
			Msg:   "tags_match must be one of any, all, any_strict, all_strict, exact",
			Type:  "value_error.enum",
			Input: req.TagsMatch,
		})
	}
	if unsupported := unsupportedTypes(req.Types); len(unsupported) > 0 {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "types"},
			Msg:   "unsupported fact type; accepted types are " + joinTokens(acceptedFactTypes),
			Type:  "value_error.enum",
			Input: unsupported,
		})
	}
	if len(req.Tags) > 0 && len(req.TagGroups.Groups) > 0 {
		items = append(items, httpx.ValidationError{
			Loc:  []string{"body", "tag_groups"},
			Msg:  "tags and tag_groups are mutually exclusive",
			Type: "value_error.mode",
		})
	}
	if invertedTemporalWindow(req.TemporalWindow) {
		items = append(items, httpx.ValidationError{
			Loc:  []string{"body", "temporal_window"},
			Msg:  "temporal_window end must not be earlier than start",
			Type: "value_error.range",
		})
	}
	if req.Limit != 0 && (req.Limit < MinLimit || req.Limit > MaxLimit) {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "limit"},
			Msg:   "limit must be between 1 and 1000",
			Type:  "value_error.range",
			Input: req.Limit,
		})
	}
	if req.Offset < MinOffset {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "offset"},
			Msg:   "offset must not be negative",
			Type:  "value_error.range",
			Input: req.Offset,
		})
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

// bodyFailure reports a malformed request body at the request boundary.
func bodyFailure() *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{{
		Loc:  []string{"body"},
		Msg:  "request body is not valid JSON",
		Type: "value_error.json",
	}}}
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
