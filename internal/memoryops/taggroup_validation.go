package memoryops

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// tagGroupUnionBranches names the branches of the reference's `TagGroup` union
// in the order it tries them, with the member each branch requires: a left-to-
// right union reports every branch that refused the value, so their order is
// part of the response.
var tagGroupUnionBranches = []struct{ name, member string }{
	{"TagGroupLeaf", "tags"},
	{"TagGroupAnd", "and"},
	{"TagGroupOr", "or"},
	{"TagGroupNot", "not"},
}

// tagGroupLeafLiterals are the leaf's two optional literal fields in the
// reference's declaration order, each with the members it accepts. Both carry
// defaults, so an omitted member takes its default and is never an item; a
// supplied one outside its set is, whatever its JSON kind.
var tagGroupLeafLiterals = []struct {
	name   string
	values []string
}{
	{"match", storage.RecallTagMatchModes()},
	{"resolve", []string{storage.TagResolveExact, storage.TagResolveFuzzy}},
}

// tagGroupTree diagnoses the one field whose model shape no Go decoder can
// hold: `tag_groups: list[TagGroup]`, where each group is the left-to-right
// union of a leaf object and the three compound objects, and each compound
// member is in turn a required list of groups (or, for `not`, one group).
//
// The reference validates such a filter branch by branch and collects EVERY
// branch's items, so the failure is a tree of items rather than the single
// "this is not what the field means" message a decoder produces. The
// diagnostics are kept apart from the decoded tree: the tree decodes best
// effort so a valid filter still reaches the store, and the caller reports
// these items instead when there are any.
type tagGroupTree struct {
	// items holds every item the union raised, in branch order.
	items []httpx.ValidationError

	// present records that the body stated the field at all; null counts as
	// omitting it, which is what the reference's default-as-None field means.
	present bool

	// list is false when the value the field arrived as was not a list at all.
	// That is the reference's own `list_type` failure, whose input is the value
	// itself, so the caller reports it rather than any union item.
	list bool

	// input is the raw value the field arrived as, which a list-type failure
	// names as its input.
	input any
}

// diagnoseTagGroups runs the union over the field's raw value. It is called
// after the ordinary decode has accepted the body, so it only ever adds items
// the decoder could not produce.
func diagnoseTagGroups(raw json.RawMessage) tagGroupTree {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return tagGroupTree{}
	}
	tree := tagGroupTree{present: true}
	_ = json.Unmarshal(trimmed, &tree.input)
	var groups []json.RawMessage
	if err := json.Unmarshal(trimmed, &groups); err != nil {
		// The field's own type is a list: anything else is the list failure,
		// reported at the field with the value as its input.
		return tree
	}
	tree.list = true
	for index, group := range groups {
		tree.items = append(tree.items,
			tagGroupUnionItems([]string{"body", "tag_groups", strconv.Itoa(index)}, group)...)
	}
	return tree
}

// tagGroupUnionItems reports one group value the way the reference's left-to-
// right union does: it tries the four branches in declaration order, collecting
// each branch's items while none of them has produced an item, so a value no
// branch accepts reports the whole tree of items in branch order — including
// the branches whose own member is absent, which are what makes the failure
// readable — and a valid value reports nothing at all.
func tagGroupUnionItems(loc []string, raw json.RawMessage) []httpx.ValidationError {
	input := jsonValueOf(raw)
	object := tagGroupObject(raw)
	var items []httpx.ValidationError
	for _, branch := range tagGroupUnionBranches {
		branchItems := tagGroupBranchItems(appendMember(loc, branch.name), object, branch, input)
		if len(branchItems) == 0 {
			return nil
		}
		items = append(items, branchItems...)
	}
	return items
}

// tagGroupBranchItems reports one branch's items for a group value. A model
// branch reports a value that is not an object as a shape failure at its own
// path and reports an object member by member; a required member that is
// absent, or whose own type is wrong, is a single item either way.
func tagGroupBranchItems(loc []string, object map[string]json.RawMessage, branch struct{ name, member string }, input any) []httpx.ValidationError {
	if object == nil {
		return []httpx.ValidationError{httpx.ValidationItem("model_attributes_type", loc, includeShapeMessage, input, nil)}
	}
	value, present := object[branch.member]
	if !present {
		return []httpx.ValidationError{httpx.Missing(appendMember(loc, branch.member), input)}
	}
	switch branch.name {
	case "TagGroupLeaf":
		return tagGroupLeafItems(loc, object, value)
	case "TagGroupNot":
		return tagGroupNotItems(appendMember(loc, branch.member), value)
	default:
		return tagGroupChildListItems(appendMember(loc, branch.member), value)
	}
}

// tagGroupLeafItems reports the leaf's own field failures. `tags` is required
// and is a list; `match` and `resolve` are literals with defaults, so a value
// outside them is a mismatch even when the member is omitted — but an omitted
// member takes its default, which is inside the literal, so it is never an
// item. A non-string value fails the literal check with the value as its input.
func tagGroupLeafItems(loc []string, object map[string]json.RawMessage, tags json.RawMessage) (result []httpx.ValidationError) {
	var items []httpx.ValidationError
	var entries []json.RawMessage
	if err := json.Unmarshal(tags, &entries); err != nil {
		items = append(items, httpx.TypeMismatch("list_type", appendMember(loc, "tags"), jsonValueOf(tags)))
	} else {
		for index, entry := range entries {
			if _, ok := jsonText(entry); !ok {
				items = append(items, httpx.TypeMismatch("string_type",
					appendMember(appendMember(loc, "tags"), strconv.Itoa(index)), jsonValueOf(entry)))
			}
		}
	}
	for _, member := range tagGroupLeafLiterals {
		value, present := object[member.name]
		if !present {
			continue
		}
		if text, ok := jsonText(value); ok && containsToken(member.values, text) {
			continue
		}
		items = append(items, httpx.Literal(appendMember(loc, member.name), jsonValueOf(value), member.values))
	}
	return items
}

// tagGroupChildListItems reports an `and`/`or` member: a required list of
// groups, whose entries recurse into the union.
func tagGroupChildListItems(loc []string, value json.RawMessage) []httpx.ValidationError {
	var entries []json.RawMessage
	if err := json.Unmarshal(value, &entries); err != nil {
		return []httpx.ValidationError{httpx.TypeMismatch("list_type", loc, jsonValueOf(value))}
	}
	var items []httpx.ValidationError
	for index, entry := range entries {
		items = append(items, tagGroupUnionItems(appendMember(loc, strconv.Itoa(index)), entry)...)
	}
	return items
}

// tagGroupNotItems reports a `not` member: one group, not a list of them. The
// member's model reports a value that is not an object by trying its child
// union against it, so the failure lands on each branch rather than on the
// member itself.
func tagGroupNotItems(loc []string, value json.RawMessage) []httpx.ValidationError {
	return tagGroupUnionItems(loc, value)
}

// tagGroupObject reads a value as a JSON object the union branches can extract
// members from, reporting nil for any other JSON value.
//
// The value is trimmed with bytes.TrimSpace, which returns a slice over the same
// buffer rather than a copy: a json.RawMessage is itself a view into the request
// body, and unmarshalling a re-sliced view can see the rest of the buffer as
// trailing content.
func tagGroupObject(raw json.RawMessage) map[string]json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return nil
	}
	return object
}

// appendMember appends a JSON path segment to a location copy, so one branch's
// path never aliases another's.
func appendMember(loc []string, member string) []string {
	out := make([]string, len(loc), len(loc)+1)
	copy(out, loc)
	return append(out, member)
}

// tagGroupFailure reports a tag-group filter whose value the request's own type
// rejected outright (the decoder's error, when the union's diagnostics did not
// name the placement).
func tagGroupFailure() *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.ValueError([]string{"body", "tag_groups"}, nil, ErrMalformedTagGroup.Error()),
	}}
}

// jsonText reports the text of a JSON string value, and false for every other
// JSON kind (including null).
func jsonText(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", false
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err != nil {
		return "", false
	}
	return text, true
}

// jsonValueOf decodes a raw value into the Go value an item's input reports,
// keeping integers exact so a large number is not rounded through float64 and
// booleans stay booleans.
func jsonValueOf(raw json.RawMessage) any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	if number, ok := value.(json.Number); ok {
		// An integral literal is reported as a JSON number, not as the string a
		// big integer would survive as; the reference echoes the value it read.
		if _, ok := new(big.Int).SetString(string(number), 10); ok && !hasFraction(number) {
			return number
		}
	}
	return value
}

// hasFraction reports whether a JSON number carries a fraction or exponent, so
// an integral literal stays an integer in the item's input.
func hasFraction(number json.Number) bool {
	for _, r := range string(number) {
		if r == '.' || r == 'e' || r == 'E' {
			return true
		}
	}
	return false
}

// constrains reports whether a tag-group node is one of the reference's union
// branches: a leaf (one that states `tags`, even as an empty list, which the
// reference reads as "untagged only" under `exact` and as "tagged at all" under
// its default mode), or one of the compound members. A node
// that is none of them matches no branch, so it is reported and never decoded —
// it would silently widen a filter otherwise, the one direction a filter must
// never fail in.
func (g TagGroupRequest) constrains() bool {
	if g.tagsGiven || len(g.Tags) > 0 {
		return true
	}
	return g.andGiven || g.orGiven || g.Not.given
}

// toStorageTagNot converts a not member, which holds at most one node.
func toStorageTagNot(n TagGroupNot) *storage.TagGroup {
	if n.Child == nil {
		return nil
	}
	converted := toStorageTagGroups([]TagGroupRequest{*n.Child})[0]
	return &converted
}

// toStorageTagGroups converts the decoded request groups into the storage
// shape.
func toStorageTagGroups(groups []TagGroupRequest) []storage.TagGroup {
	out := make([]storage.TagGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, storage.TagGroup{
			And:     toStorageTagGroups(g.And),
			Or:      toStorageTagGroups(g.Or),
			Not:     toStorageTagNot(g.Not),
			Tag:     g.Tag,
			Tags:    g.Tags,
			Resolve: g.Resolve,
			Match:   g.Match,
		})
	}
	return out
}

// reflectFactTypeItems reports the reflection request's fact-type selection
// the way its model does. The field is
// `list[Literal["world", "experience", "observation"]]`, so each element is
// checked on its own: a value outside the literal set is a `literal_error` at
// that element's index with the offending value as its input — a number is that
// failure too, because the literal check runs before any string coercion. When
// the body's raw selection cannot be read element by element the already-decoded
// selection is used, so the items still name the right indices.
func reflectFactTypeItems(body map[string]any) []httpx.ValidationError {
	raw, present := body["fact_types"]
	if !present || raw == nil {
		return nil
	}
	entries, isList := raw.([]any)
	if !isList {
		// A non-list value is the field's own type failure, which the body walk
		// already reported.
		return nil
	}
	var items []httpx.ValidationError
	for index, entry := range entries {
		text, isText := entry.(string)
		if isText && containsToken(acceptedFactTypes, text) {
			continue
		}
		items = append(items, httpx.Literal([]string{"body", "fact_types", strconv.Itoa(index)},
			entry, acceptedFactTypes))
	}
	return items
}

// recallTypeElements reports the recall request's fact-type selection the way
// its model does before the handler filters it. The field is a plain
// `list[str]`, so an element that is not a string is a `string_type` failure at
// that element's own index and the model never reaches the route's own "this is
// not a fact type" rule for the others.
func recallTypeElements(body map[string]any) []httpx.ValidationError {
	raw, present := body["types"]
	if !present || raw == nil {
		return nil
	}
	entries, isList := raw.([]any)
	if !isList {
		return nil
	}
	var items []httpx.ValidationError
	for index, entry := range entries {
		if _, isText := entry.(string); isText {
			continue
		}
		items = append(items, httpx.TypeMismatch("string_type",
			[]string{"body", "types", strconv.Itoa(index)}, entry))
	}
	return items
}
