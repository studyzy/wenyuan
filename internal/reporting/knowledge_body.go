package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

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
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, httpx.BodyDecodeFailure(err, raw)
	}
	decoded, err := httpx.DecodeLax(raw, target)
	httpx.RecordUnknownBodyFields(r, decoded, target)
	if len(bytes.TrimSpace(decoded)) == 0 {
		return nil, httpx.BodyDecodeFailure(httpx.ErrEmptyBody, decoded)
	}
	if err != nil {
		return nil, httpx.BodyDecodeFailure(err, decoded)
	}
	object := map[string]any{}
	if err := json.Unmarshal(decoded, &object); err != nil {
		return nil, httpx.BodyDecodeFailure(err, decoded)
	}
	return object, nil
}

// knowledgeCreateRequiredNames the create body's required members, which differ
// between the two forms: a page also has to state the question it answers.
func knowledgeCreateRequiredNames(kind storage.KnowledgeKind) []string {
	if kind == knowledgeKindPage {
		return []string{"name", "source_query"}
	}
	return []string{"name"}
}

// decodeKnowledgeCreateBody decodes a create body and validates its required
// members the way the reference's model does: an absent member is a `missing`
// item and an explicit null a `string_type` one, in declaration order.
func decodeKnowledgeCreateBody(r *http.Request, target *CreateKnowledgeNodeRequest, kind storage.KnowledgeKind) *httpx.ValidationFailure {
	object, failure := decodeKnowledgeBody(r, target)
	if failure != nil {
		return failure
	}
	items := make([]httpx.ValidationError, 0, 2)
	for _, field := range knowledgeCreateRequiredNames(kind) {
		value, present := object[field]
		switch {
		case !present:
			items = append(items, httpx.Missing([]string{"body", field}, object))
		case value == nil:
			items = append(items, httpx.TypeMismatch("string_type", []string{"body", field}, nil))
		}
	}
	if len(items) > 0 {
		return &httpx.ValidationFailure{Errors: items}
	}
	return nil
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
