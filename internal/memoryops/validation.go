package memoryops

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// mergeRouteItems merges the items the shared decoder reported for a body with
// the items the route's own validators derived from the same bytes, and returns
// the single failure the reference serves.
//
// The reference validates a request in one pass: a rule a field's validator
// enforces appears beside the items the sibling fields produced. A rule the
// model enforces on the request as a whole runs only after every field has
// validated, so it is not reached at all once any field failed — a route item
// whose location is the body itself is therefore dropped whenever the decoder
// reported anything. A field-level route item the decoder already named is
// dropped as well, so one field is reported once. The survivors are then put in
// the model's field-declaration order, which is the order the reference reports
// them in.
func mergeRouteItems(r *http.Request, target any, decoded, route *httpx.ValidationFailure) *httpx.ValidationFailure {
	items := append([]httpx.ValidationError(nil), decoded.Errors...)
	if route != nil {
		for _, item := range route.Errors {
			if len(item.Loc) < 2 || locationCovered(item.Loc, decoded.Errors) {
				continue
			}
			items = append(items, item)
		}
	}
	httpx.OrderItemsByDeclaration(r, target, items)
	return &httpx.ValidationFailure{Errors: items}
}

// locationCovered reports whether the decoder already reported the field at
// loc, either at that exact location or on a path it encloses.
func locationCovered(loc []string, items []httpx.ValidationError) bool {
	for _, item := range items {
		if len(item.Loc) <= len(loc) && equalPath(item.Loc, loc[:len(item.Loc)]) {
			return true
		}
	}
	return false
}

// equalPath reports whether two locations name the same path.
func equalPath(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// recallDecodeFailure renders a rejected recall body. The shared decoder stops
// at the field failures its walk reported, before it fills the model, so the
// model is refilled from the same bytes for the route's own field-level rules
// and the two sets are merged the way the reference's single validation pass
// reports them.
func recallDecodeFailure(r *http.Request, req *RecallRequest, body []byte, err error) *httpx.ValidationFailure {
	// The walk names every field the reference's model refuses, in one pass;
	// the Go decoder stops at the first, so its items are the ones to report
	// whenever it can describe the body.
	items := httpx.ValidationItemsAgainstRequest(r, req, body)
	if len(items) == 0 {
		return httpx.BodyDecodeFailure(err, body)
	}
	// The model is refilled from the same bytes so the route's own field-level
	// rules see the values the caller sent.
	_ = json.Unmarshal(body, req)
	return mergeRouteItems(r, req, &httpx.ValidationFailure{Errors: items},
		ValidateRecall(*req, requestBodyObject(body)))
}

// reflectDecodeFailure is recallDecodeFailure for the reflection route, which
// carries one extra field-level rule (the structured-output schema) behind the
// handler's validator.
func reflectDecodeFailure(r *http.Request, req *ReflectRequest, body []byte, err error, schemaValidator ReflectSchemaValidator) *httpx.ValidationFailure {
	items := httpx.ValidationItemsAgainstRequest(r, req, body)
	if len(items) == 0 {
		return httpx.BodyDecodeFailure(err, body)
	}
	_ = json.Unmarshal(body, req)
	return mergeRouteItems(r, req, &httpx.ValidationFailure{Errors: items},
		ValidateReflect(*req, requestBodyObject(body), schemaValidator))
}

// ValidateRetain checks the retain request fields, including the required item
// list, the per-item member types the reference's model pins, and the
// operation-identifier shape. The multi-strategy and duplicate-document rules
// are the reference's handler-level 400s, not model validators, so they are
// enforced on the write path rather than here.
func ValidateRetain(req RetainRequest, body any) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	if req.Items == nil {
		// The framework reports a missing field with the object it was missing
		// from as the item's input; here that object is the whole request body,
		// so a client that sent `{"async":false}` is answered with exactly that.
		items = append(items, httpx.Missing([]string{"body", "items"}, body))
	}
	for index, item := range req.Items {
		items = append(items, itemContentErrors(index, item)...)
		items = append(items, itemFieldErrors(index, item)...)
	}
	// The reference validates this field with `uuid.UUID(value)`, which accepts
	// the lax spellings (32 hex digits, braces, the urn prefix) and rejects the
	// rest; the canonical spelling is what the operation is stored under.
	if req.OperationID != "" {
		if _, ok := canonicalOperationID(req.OperationID); !ok {
			items = append(items, httpx.ValueError([]string{"body", "operation_id"}, req.OperationID,
				"operation_id must be a valid UUID"))
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}

// scopeUnionErrors renders the failures the reference reports for a value that
// fits neither branch of the observation_scopes union. The union's first branch
// is the literal set of scope modes and its second is a list of tag groups, so a
// rejected value names both branches in that order: the literal failure carries
// the whole value, and the list failure is reported once for the value itself
// when it is not a list, or once per element that is not a group, with a
// non-string tag inside a group named at its own position.
func scopeUnionErrors(fieldLoc []string, value any) []httpx.ValidationError {
	items := []httpx.ValidationError{
		httpx.Literal(append(clonePath(fieldLoc), scopeLiteralBranch), value, ObservationScopeModes),
	}
	groups, isList := value.([]any)
	if !isList {
		return append(items, httpx.TypeMismatch("list_type", append(clonePath(fieldLoc), scopeListBranch), value))
	}
	for index, element := range groups {
		elementLoc := append(append(clonePath(fieldLoc), scopeListBranch), strconv.Itoa(index))
		tags, isGroup := element.([]any)
		if !isGroup {
			items = append(items, httpx.TypeMismatch("list_type", elementLoc, element))
			continue
		}
		for position, tag := range tags {
			if _, isText := tag.(string); !isText {
				items = append(items, httpx.TypeMismatch("string_type",
					append(clonePath(elementLoc), strconv.Itoa(position)), tag))
			}
		}
	}
	return items
}

// clonePath returns a copy of a location, so an item that extends it does not
// write through to the caller's slice.
func clonePath(loc []string) []string {
	return append([]string(nil), loc...)
}

// scopeLiteralBranch and scopeListBranch are the names the framework gives the
// observation_scopes union's two branches in a rejected value's location. The
// literal branch's name is built from the accepted modes, so it cannot drift
// from the set the field validates against.
var (
	scopeLiteralBranch = func() string {
		quoted := make([]string, 0, len(ObservationScopeModes))
		for _, mode := range ObservationScopeModes {
			quoted = append(quoted, "'"+mode+"'")
		}
		return "literal[" + strings.Join(quoted, ",") + "]"
	}()
	scopeListBranch = "list[list[str]]"
)

// itemContentErrors reports what the reference's content validator rejects for
// one item's content value. That value is a `str | list[blocks]` union carrying
// a field validator, so the list arm's block failures come first — each block
// under the union's list path, headed by the string arm's own type failure —
// and the "content cannot be empty" rejection is reached only when every block
// validated, because a field validator runs after the type validation.
func itemContentErrors(index int, item RetainItem) []httpx.ValidationError {
	contentLoc := []string{"body", "items", strconv.Itoa(index), "content"}
	content := item.Content
	if !content.Present() {
		// The member itself is absent, which the model reports before the
		// value validator can run.
		return []httpx.ValidationError{httpx.Missing(contentLoc, item.rawObject())}
	}
	if len(content.blockIssues) > 0 {
		items := []httpx.ValidationError{httpx.TypeMismatch("string_type",
			append(clonePath(contentLoc), "str"), content.rawValue())}
		for _, issue := range content.blockIssues {
			loc := append(clonePath(contentLoc), issue.Loc...)
			items = append(items, httpx.ValidationItem(issue.Kind, loc, issue.Msg, issue.Input, issue.Ctx))
		}
		return items
	}
	if content.IsList && len(content.Blocks) == 0 {
		// An empty list is still the list arm's value, so its "cannot be empty"
		// rejection carries the list itself.
		return []httpx.ValidationError{httpx.ValueError(contentLoc, []any{}, "content cannot be empty")}
	}
	if !content.CarriesContent() {
		return []httpx.ValidationError{httpx.ValueError(contentLoc, content.rawValue(), "content cannot be empty")}
	}
	return nil
}

// itemFieldErrors reports the per-item failures the reference's model raises at
// the item's own field path, in the model's field-declaration order (timestamp,
// metadata, entities, observation_scopes, update_mode). Each check records its
// failure during decode instead of aborting, so a whole body is still described
// field by field, the way the reference reports.
func itemFieldErrors(index int, item RetainItem) []httpx.ValidationError {
	var items []httpx.ValidationError
	path := func(member string) []string {
		return []string{"body", "items", strconv.Itoa(index), member}
	}
	if item.Timestamp.Invalid {
		items = append(items, httpx.ValueError(
			path("timestamp"), item.Timestamp.InvalidInput, item.Timestamp.InvalidMessage))
	}
	for _, issue := range item.metadataIssues {
		loc := path("metadata")
		if issue.key != "" {
			loc = append(loc, issue.key)
		}
		items = append(items, httpx.TypeMismatch(issue.kind, loc, issue.input))
	}
	for position, entity := range item.Entities {
		if entity.Invalid {
			items = append(items, httpx.ValidationItem("model_attributes_type",
				append(path("entities"), strconv.Itoa(position)),
				"Input should be a valid dictionary or object to extract fields from",
				entity.InvalidInput, nil))
			continue
		}
		for _, issue := range entity.memberIssues {
			loc := append(append(path("entities"), strconv.Itoa(position)), issue.field)
			if issue.kind == "missing" {
				items = append(items, httpx.Missing(loc, issue.input))
				continue
			}
			items = append(items, httpx.TypeMismatch(issue.kind, loc, issue.input))
		}
	}
	for _, issue := range item.tagIssues {
		items = append(items, httpx.TypeMismatch("string_type",
			append(path("tags"), strconv.Itoa(issue.index)), issue.input))
	}
	if item.ObservationScopes.Invalid {
		items = append(items, scopeUnionErrors(path("observation_scopes"), item.ObservationScopes.InvalidInput)...)
	}
	if item.updateModeInvalid {
		items = append(items, httpx.Literal(path("update_mode"), item.updateModeInput, updateModeLiteral))
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

// duplicateDocumentIDsInGroups returns the duplicated non-empty document
// identifiers of the first strategy group that carries any, in first-appearance
// order, or nil when no group has a duplicate. The reference fans an async
// request out one submit per strategy group and checks each group's own items,
// so a document_id repeated across two different strategies is not a duplicate —
// only a repeat inside one group is.
func duplicateDocumentIDsInGroups(items []RetainItem) []string {
	groupOrder := make([]string, 0)
	groups := make(map[string][]RetainItem)
	for _, item := range items {
		if _, seen := groups[item.Strategy]; !seen {
			groupOrder = append(groupOrder, item.Strategy)
		}
		groups[item.Strategy] = append(groups[item.Strategy], item)
	}
	for _, strategy := range groupOrder {
		if duplicates := duplicateDocumentIDs(groups[strategy]); len(duplicates) > 0 {
			return duplicates
		}
	}
	return nil
}

// duplicateDocumentIDs returns the non-empty document identifiers that appear
// more than once, each once, in first-appearance order.
func duplicateDocumentIDs(items []RetainItem) []string {
	counts := make(map[string]int)
	order := make([]string, 0)
	for _, item := range items {
		if item.DocumentID == "" {
			continue
		}
		if counts[item.DocumentID] == 0 {
			order = append(order, item.DocumentID)
		}
		counts[item.DocumentID]++
	}
	duplicates := make([]string, 0)
	for _, id := range order {
		if counts[id] > 1 {
			duplicates = append(duplicates, id)
		}
	}
	return duplicates
}

// includeShapeMessage is the framework's wording for a value that is not the
// object a model field extracts its own fields from.
const includeShapeMessage = "Input should be a valid dictionary or object to extract fields from"

// requestBodyObject decodes a body the handler already read into the object
// shape a validation item names as its own input, so a `missing` item reports
// the body the caller sent.
func requestBodyObject(body []byte) map[string]any {
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return map[string]any{}
	}
	return object
}

// queryFieldErrors reports the query checks the reference runs as a field
// validator: the field is required, "null" is not a string, and a supplied
// string must carry at least one word character. The body object is the input a
// missing-field item names, and the word-character rule applies only when a
// string was actually supplied.
func queryFieldErrors(body map[string]any, query string) []httpx.ValidationError {
	raw, present := body["query"]
	switch {
	case !present:
		return []httpx.ValidationError{httpx.Missing([]string{"body", "query"}, body)}
	case raw == nil:
		return []httpx.ValidationError{httpx.TypeMismatch("string_type", []string{"body", "query"}, nil)}
	case !hasWordCharacter(query):
		return []httpx.ValidationError{httpx.ValueError([]string{"body", "query"}, query,
			"query must contain at least one word character after normalization")}
	}
	return nil
}

// includeMemberErrors reports every include-member failure the reference reports
// for one request, in member order. The include value itself, when present and
// neither an object nor null, is a single shape failure. Otherwise each named
// member is checked in declaration order: a member that is present, non-null and
// not an object is a shape failure, and a member object whose own fields failed
// to decode contributes every item its decoder recorded, in field order. An
// absent or null member is a legitimate "disabled" value, so it is skipped.
//
// The two kinds interleave by member because the reference validates each member
// in field order: a shape failure on an early member precedes a decode failure on
// a later one, and vice versa.
func includeMemberErrors(body map[string]any, include includeMemberFailureSource, members []string) []httpx.ValidationError {
	raw, present := body["include"]
	if !present {
		return nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return []httpx.ValidationError{includeShapeError([]string{"body", "include"}, raw)}
	}
	var items []httpx.ValidationError
	for _, name := range members {
		member, present := object[name]
		if !present || member == nil {
			continue
		}
		if _, ok := member.(map[string]any); !ok {
			items = append(items, includeShapeError([]string{"body", "include", name}, member))
			continue
		}
		if memberItems, ok := include.memberFailure(name); ok {
			items = append(items, memberItems...)
		}
	}
	return items
}

// includeShapeError builds the item the framework emits for a value that is not
// the dictionary a model field expects.
func includeShapeError(loc []string, input any) httpx.ValidationError {
	return httpx.ValidationItem("model_attributes_type", loc, includeShapeMessage, input, nil)
}

// recallIncludeMembers names the recall include sub-object's model members, in
// the reference's field order.
var recallIncludeMembers = []string{"entities", "chunks", "source_facts"}

// ValidateRecall checks the recall request fields: the required query, the
// include sub-object's shape, the fact-type selection, the budget level, the
// tag-scoping form, the tag-matching mode and the pagination bounds.
func ValidateRecall(req RecallRequest, body map[string]any) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	items = append(items, queryFieldErrors(body, req.Query)...)
	if req.Budget != "" && !containsToken(storageBudgetLevels(), req.Budget) {
		items = append(items, httpx.Enum([]string{"body", "budget"}, req.Budget, storageBudgetLevels()))
	}
	items = append(items, includeMemberErrors(body, req.Include, recallIncludeMembers)...)
	// The selection is a `list[str]`: an element of another JSON kind is a
	// field-level failure, and a model whose field failed skips its validator,
	// so the route's own "not a fact type" rule does not run alongside it.
	typeItems := recallTypeElements(body)
	items = append(items, typeItems...)
	if len(typeItems) == 0 {
		if unsupported := unsupportedTypes(req.Types); len(unsupported) > 0 {
			items = append(items, httpx.ValueError([]string{"body", "types"}, unsupported,
				"unsupported fact type; accepted types are "+joinTokens(acceptedFactTypes)))
		}
	}
	if req.TagsMatch != "" && !containsToken(storageTagMatchModes(), req.TagsMatch) {
		items = append(items, httpx.Literal([]string{"body", "tags_match"}, req.TagsMatch, storageTagMatchModes()))
	}
	// The tag filter is reported at its own paths before the body-level rules:
	// the framework walks the fields before the request model validates, so a
	// group the union could not read outranks the mutual-exclusion check.
	if groupItems := req.TagGroups.Items(); len(groupItems) > 0 {
		return &httpx.ValidationFailure{Errors: groupItems}
	}
	// The reference's rule is presence, not content: `tags: []` beside a
	// `tag_groups` list is refused just as a populated one is, because the model
	// validator tests every member against None.
	if tagsSupplied(body) && req.TagGroups.Present {
		items = append(items, httpx.ValueError([]string{"body"}, body,
			"'tags' and 'tag_groups' are mutually exclusive. Use 'tag_groups' for compound filtering."))
	}
	items = append(items, temporalWindowErrors(req.TemporalWindow, body)...)
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}

// tagsSupplied reports whether the request body states `tags` at all (an
// explicit null is the same as omitting it, which is what the reference's
// default-as-None field means).
func tagsSupplied(body map[string]any) bool {
	value, present := body["tags"]
	return present && value != nil
}

// temporalWindowErrors reports the items the reference's window model raises: one
// per bound that is absent, null or not a parseable datetime, then — only once
// both bounds parsed — the range check. The window's input in the range item is
// the request body's own window object, which is what the framework names.
func temporalWindowErrors(window *TemporalWindowRequest, body map[string]any) []httpx.ValidationError {
	if window == nil {
		return nil
	}
	if len(window.fields.items) > 0 {
		return window.fields.items
	}
	if !window.fields.inverted {
		return nil
	}
	input, _ := body["temporal_window"].(map[string]any)
	return []httpx.ValidationError{httpx.ValueError([]string{"body", "temporal_window"}, input,
		"temporal_window.end must not be earlier than temporal_window.start")}
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
