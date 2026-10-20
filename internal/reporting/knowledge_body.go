package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// decodeKnowledgeBody reads and decodes a JSON request body and returns the
// decoded object alongside it, so a missing required member can name the whole
// body the way the reference's framework does. A body that is absent, not JSON,
// or wrong-typed in the target is reported as a structured validation failure
// (422), never a bare 400.
func decodeKnowledgeBody(r *http.Request, target any) (map[string]any, *httpx.ValidationFailure) {
	_, object, failure := decodeKnowledgeBodyRaw(r, target)
	return object, failure
}

// decodeKnowledgeBodyRaw is decodeKnowledgeBody with the body's own decoded
// values as well as the object the target's lax coercion produced. The raw view
// is what the reference echoes as a validation item's `input`: its framework
// reports the JSON it received, before any coercion turned "500" into a number.
func decodeKnowledgeBodyRaw(r *http.Request, target any) (map[string]any, map[string]any, *httpx.ValidationFailure) {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, httpx.BodyDecodeFailure(err, raw)
	}
	// The body's own view is read first and kept: the typed decode below consumes
	// members it cannot coerce, while the reference's model reports every wrong
	// member in one run, and a `missing` item names the body as it was written.
	verbatim := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&verbatim); err != nil {
		verbatim = nil
	}
	if verbatim != nil {
		bodyObjects.Store(r, verbatim)
	}
	// The member order is recorded too, so a failure's `input` echoes the body the
	// way the client wrote it.
	httpx.RecordBodyOrder(r, raw)
	decoded, err := httpx.DecodeLax(raw, target)
	httpx.RecordUnknownBodyFields(r, decoded, target)
	if len(bytes.TrimSpace(decoded)) == 0 {
		return verbatim, nil, httpx.BodyDecodeFailure(httpx.ErrEmptyBody, decoded)
	}
	if err != nil {
		return verbatim, nil, httpx.BodyDecodeFailure(err, decoded)
	}
	object := map[string]any{}
	if err := json.Unmarshal(decoded, &object); err != nil {
		return verbatim, nil, httpx.BodyDecodeFailure(err, decoded)
	}
	if verbatim == nil {
		verbatim = object
	}
	return verbatim, object, nil
}

// bodyObjects carries a request body's verbatim decoded object from the read to
// the validator that runs right after it, keyed by the request.
var bodyObjects sync.Map

// readKnowledgeBodyObject returns the verbatim object decodeKnowledgeBodyRaw read
// for this request, and whether there was one.
func readKnowledgeBodyObject(r *http.Request) (map[string]any, bool) {
	value, ok := bodyObjects.LoadAndDelete(r)
	if !ok {
		return nil, false
	}
	object, isObject := value.(map[string]any)
	return object, isObject
}

// knowledgeVerbatimObject returns the body's own decoded object. The typed
// decode consumes a body whose members it could not coerce without keeping the
// object it read, so the request-scoped copy is consulted first and the object
// the raw decode returned — which the same call produced — answers otherwise.
func knowledgeVerbatimObject(r *http.Request, decoded map[string]any) (map[string]any, bool) {
	if object, ok := readKnowledgeBodyObject(r); ok && len(object) > 0 {
		return object, true
	}
	if len(decoded) > 0 {
		return decoded, true
	}
	return nil, false
}

// knowledgeCreateFieldOrder is the create body's declaration order. The
// reference's model declares name, parent_id, source_query, tags, max_tokens and
// trigger, and its field checks run in that order regardless of the order the
// client wrote the members in — so a body with two bad values reports them in
// this order, not the body's.
var knowledgeCreateFieldOrder = []string{"name", "parent_id", "source_query", "tags", "max_tokens", "trigger"}

// knowledgeCreateRequiredNames name the create body's required members, which
// differ between the two forms: a page also has to state the question it
// answers.
func knowledgeCreateRequiredNames(kind storage.KnowledgeKind) []string {
	if kind == knowledgeKindPage {
		return []string{"name", "source_query"}
	}
	return []string{"name"}
}

// knowledgeCreateFieldRequired reports whether a member is required by the form
// the body belongs to.
func knowledgeCreateFieldRequired(field string, kind storage.KnowledgeKind) bool {
	for _, required := range knowledgeCreateRequiredNames(kind) {
		if required == field {
			return true
		}
	}
	return false
}

// decodeKnowledgeCreateBody decodes a create body and validates it the way the
// reference's model does.
//
// The reference validates the whole model in one pass and reports the items in
// stages: the members it found missing or null, then each declared field's own
// check, then the nested models'. Since it validates before it ever asks whether
// a member is required, a body naming no question *and* carrying a wrongly typed
// tag list reports both — the shape a "required check first" implementation gets
// wrong by reporting only one.
func decodeKnowledgeCreateBody(r *http.Request, target any, kind storage.KnowledgeKind) *httpx.ValidationFailure {
	verbatim, object, failure := decodeKnowledgeBodyRaw(r, target)
	if failure != nil && !knowledgeDecodeRejectedAField(failure) {
		// Not a body the model can be read from at all — absent, not JSON, not text,
		// too large. The framework's parser speaks before the model does, so its
		// item is the whole answer rather than one item among several. A member the
		// lax decode refused is a different matter: the model still had a body to
		// read, and reports the other members' problems alongside that item.
		return failure
	}
	// The fields are checked against the body's own view. The typed decode drops a
	// value it could not coerce, and the reference's model reports every wrong
	// member in one run rather than stopping at the first.
	fields := object
	if rawObject, ok := knowledgeVerbatimObject(r, verbatim); ok {
		fields = rawObject
	}
	// The reference reports a model's items in declaration order, one stage per
	// member: a member it found missing or received as null is reported where that
	// member is declared, and its own value checks follow at the same position.
	// That is why `{"tags":1,"name":2}` reports name before source_query, and why a
	// body naming no question but carrying a bad tag list reports both.
	var items []httpx.ValidationError
	nulled := make(map[string]bool, 2)
	// A folder body declares `name` then `parent_id`; a page body declares both
	// plus the page options, so the loop covers every member either form has.
	order := knowledgeCreateFieldOrder
	for _, field := range order {
		if kind == knowledgeKindFolder && field != "name" && field != "parent_id" {
			// The folder model does not declare the page options, so it never looks
			// at them.
			continue
		}
		required := knowledgeCreateFieldRequired(field, kind)
		value, present := fields[field]
		switch {
		case required && !present:
			items = append(items, httpx.Missing([]string{"body", field}, fields))
			continue
		case required && value == nil:
			items = append(items, httpx.TypeMismatch("string_type", []string{"body", field}, nil))
			nulled[field] = true
			continue
		}
		if nulled[field] {
			continue
		}
		items = append(items, knowledgeCreateFieldItems(field, fields, fields)...)
	}
	if len(items) > 0 {
		// A `missing` item echoes the body, which the reference reports in the order
		// the client wrote it.
		return httpx.OrderWholeBodyFailure(r, &httpx.ValidationFailure{Errors: items})
	}
	return nil
}

// knowledgeDecodeRejectedAField reports whether a decode failure is one member
// the lax decode refused — an item whose loc names a body member — rather than a
// body the parser could not read at all.
//
// The two are told apart by the item's type: the framework's parser failure is
// `json_invalid`, whichever position in the text it reports (a body that is not
// JSON at all fails at position 0, which reaches here as the loc `["body", 0]`
// and must not be mistaken for the member named "0"). Every other item is the
// field's own check.
func knowledgeDecodeRejectedAField(failure *httpx.ValidationFailure) bool {
	for _, item := range failure.Errors {
		if item.Type != "json_invalid" {
			return true
		}
	}
	return false
}

// knowledgeCreateFieldItems validates one create-body member against the
// reference model's declaration for it and returns the items it was refused
// with. An absent or null member is not a field failure — null is either the
// member's own nullability or the required check above.
func knowledgeCreateFieldItems(field string, object, verbatim map[string]any) []httpx.ValidationError {
	raw, present := object[field]
	if !present || raw == nil {
		return nil
	}
	switch field {
	case "name", "source_query":
		if _, ok := raw.(string); !ok {
			return []httpx.ValidationError{httpx.TypeMismatch("string_type", []string{"body", field}, verbatim[field])}
		}
	case "tags":
		list, ok := raw.([]any)
		if !ok {
			return []httpx.ValidationError{httpx.TypeMismatch("list_type", []string{"body", "tags"}, raw)}
		}
		var items []httpx.ValidationError
		for index, entry := range list {
			if _, ok := entry.(string); !ok {
				items = append(items, httpx.TypeMismatch("string_type",
					[]string{"body", "tags", strconv.Itoa(index)}, entry))
			}
		}
		return items
	case "parent_id":
		if _, ok := raw.(string); !ok {
			return []httpx.ValidationError{httpx.TypeMismatch("string_type",
				[]string{"body", "parent_id"}, verbatim[field])}
		}
	case "max_tokens":
		// The reference's int field takes the framework's lax coercion and its
		// unbounded Python range, so the parser the query parameters use answers
		// here too: "500" and 500.0 are numbers, "abc" is not.
		if _, ok := frameworkIntFromString(pythonIntegerRepr(raw)); !ok {
			return []httpx.ValidationError{httpx.TypeMismatch("int_parsing",
				[]string{"body", "max_tokens"}, verbatim[field])}
		}
	case "trigger":
		// The trigger is a nested model, so its own checks produce its own items;
		// the reference reports them after the required members and the sibling
		// fields, in the same run.
		trigger, ok := raw.(map[string]any)
		if !ok {
			// A nested model is announced by its own failure: the reference's
			// model_attributes_type says the value is not an object to read fields
			// from, which is a different item from a plain dict_type.
			return []httpx.ValidationError{httpx.TypeMismatch("model_attributes_type",
				[]string{"body", "trigger"}, verbatim[field])}
		}
		if failure := mentalmodels.ValidateTriggerObject(trigger); failure != nil {
			return failure.Errors
		}
	}
	return nil
}

// pythonIntegerRepr renders a decoded JSON value the way the reference's lax
// integer coercion reads it: a number as its Python text form (so 500 and 500.0
// both become a digit run) and anything else as the raw JSON text it arrived as.
func pythonIntegerRepr(raw any) string {
	if number, ok := raw.(json.Number); ok {
		return number.String()
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// knowledgeParentFromBody reads a `parent_id` member. An absent member and an
// explicit null both mean "no parent"; an empty string is a real identifier the
// caller sent, and is looked up like any other. A wrong-typed value is the
// framework's `string_type` item.
func knowledgeParentFromBody(raw json.RawMessage) (*string, *httpx.ValidationFailure) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		var input any
		_ = json.Unmarshal(raw, &input)
		return nil, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.TypeMismatch("string_type", []string{"body", "parent_id"}, input)}}
	}
	return &value, nil
}

// knowledgeUpdateTrigger reads a `trigger` member of an update body. The
// reference treats an absent member, an explicit null and an empty object alike
// — none of them is a page option — so only a non-empty object is returned as a
// supplied patch.
func knowledgeUpdateTrigger(raw json.RawMessage) (map[string]any, bool, *httpx.ValidationFailure) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, false, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.TypeMismatch("dict_type", []string{"body", "trigger"}, nil)}}
	}
	if len(object) == 0 {
		return nil, false, nil
	}
	if failure := mentalmodels.ValidateTriggerObject(object); failure != nil {
		return nil, false, failure
	}
	return object, true, nil
}

// assertKnowledgeFolderParent enforces the reference's parent rule: a non-null
// parent must be an existing folder in the same bank. A missing row and a row
// that is not a folder are both the caller's 400, with the reference's wording;
// any other storage failure stays a server fault.
func (h *Handlers) assertKnowledgeFolderParent(ctx context.Context, bankID, parentID string) *httpx.HTTPError {
	node, err := h.Store.GetKnowledgeNode(ctx, bankID, parentID)
	if errors.Is(err, storage.ErrKnowledgeNodeNotFound) {
		return httpx.NewHTTPError(httpx.KindBadRequest, "Parent folder '"+parentID+"' not found")
	}
	if err != nil {
		return asHTTPError(err)
	}
	if node.Kind != knowledgeKindFolder {
		return httpx.NewHTTPError(httpx.KindBadRequest, "Parent '"+parentID+"' is not a folder")
	}
	return nil
}

// assertKnowledgeMove refuses a move that would create a parent cycle. The walk
// climbs from the destination; reaching the node itself means the destination
// sits inside its own subtree. A `seen` set bounds the climb, because a tree
// corrupted before this rule existed is not forbidden by the foreign key and
// would otherwise spin forever.
func (h *Handlers) assertKnowledgeMove(ctx context.Context, bankID, nodeID, newParent string) *httpx.HTTPError {
	nodes, err := h.Store.ListKnowledgeNodes(ctx, bankID)
	if err != nil {
		return asHTTPError(err)
	}
	parentOf := make(map[string]string, len(nodes))
	for _, node := range nodes {
		parentOf[node.ID] = node.ParentID
	}
	seen := map[string]bool{}
	for cursor := newParent; cursor != ""; cursor = parentOf[cursor] {
		if cursor == nodeID {
			return httpx.NewHTTPError(httpx.KindBadRequest, "Cannot move a node into its own subtree")
		}
		if seen[cursor] {
			return httpx.NewHTTPError(httpx.KindBadRequest,
				"Knowledge tree for bank '"+bankID+"' contains a parent cycle")
		}
		seen[cursor] = true
	}
	return nil
}

// knowledgeNotFound maps a node read failure: a missing node is the reference's
// route-specific detail text, anything else the shared mapper.
func knowledgeNotFound(err error, detail string) *httpx.HTTPError {
	if errors.Is(err, storage.ErrKnowledgeNodeNotFound) {
		return httpx.NewHTTPError(httpx.KindNotFound, detail)
	}
	return asHTTPError(err)
}

// knowledgeNodeNotFound is the reference's detail for an unknown node id.
func knowledgeNodeNotFound(nodeID string) string {
	return "Knowledge node '" + nodeID + "' not found"
}

// knowledgePageNotFound is the reference's detail for an unknown page id.
func knowledgePageNotFound(pageID string) string {
	return "Knowledge page '" + pageID + "' not found"
}

// derefString reads a nullable string, empty when absent.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// derefStrings reads a nullable tag list, empty when absent.
func derefStrings(values *[]string) []string {
	if values == nil {
		return nil
	}
	return *values
}
