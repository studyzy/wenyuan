package reporting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// This file reproduces the reference's `_TAG_GROUPS_ADAPTER` — a Pydantic
// `TypeAdapter(list[TagGroup])` — for the `tag_groups` query parameter that the
// knowledge tree and search share.
//
// The reference declares TagGroup as a left-to-right union
// (engine/search/tags.py:343-346):
//
//	TagGroup = Annotated[
//	    TagGroupLeaf | TagGroupAnd | TagGroupOr | TagGroupNot,
//	    Field(union_mode="left_to_right"),
//	]
//
// A union of that mode tries every member in order and, when none validates,
// reports the failures of *all* of them — so a single malformed object yields
// four errors (one per member), not one. The message is Pydantic's
// `ValidationError.__str__`, which the handler wraps as
// `HTTPException(422, detail=f"Invalid tag_groups: {e}")`
// (api/http.py:3715-3719). Reconstructing it here is what keeps the 422 body
// byte-identical; accepting any JSON instead would let a filter the reference
// refuses through, and the tree would answer a request the reference rejects.

// tagGroupUnionMember is one member of the reference's TagGroup union, in the
// order Pydantic tries them.
type tagGroupUnionMember struct {
	// name is the model's name, which prefixes each error's location.
	name string
	// field is the member's single required field, under its JSON alias: a leaf
	// requires `tags`; the compound members require `and`, `or`, `not`.
	field string
	// list reports whether the required field holds a JSON list of nested
	// TagGroups. Only `and` and `or` do — `not` is declared `filter: TagGroup`,
	// a single nested group, so a value there is validated as one union.
	list bool
}

// tagGroupUnionMembers is the union, in declaration order. TagGroupLeaf is
// first because Pydantic identifies a leaf by the presence of `tags`.
var tagGroupUnionMembers = []tagGroupUnionMember{
	{name: "TagGroupLeaf", field: "tags"},
	{name: "TagGroupAnd", field: "and", list: true},
	{name: "TagGroupOr", field: "or", list: true},
	{name: "TagGroupNot", field: "not"},
}

// tagGroupUnionTitle is the validator's title, as Pydantic renders it from the
// annotated union: the members' names inside `union[...]`, nested in `list[...]`
// because the adapter validates a list of them.
const tagGroupUnionTitle = "list[union[TagGroupLeaf,TagGroupAnd,TagGroupOr,TagGroupNot]]"

// tagGroupLeafMatchModes and tagGroupLeafResolveModes are the Literals the leaf
// declares (`match: TagsMatch = "any_strict"`, `resolve: TagResolution = "exact"`).
var (
	tagGroupLeafMatchModes = []string{
		storage.TagsMatchAny, storage.TagsMatchAll,
		storage.TagsMatchAnyStrict, storage.TagsMatchAllStrict,
		storage.TagsMatchExact,
	}
	tagGroupLeafResolveModes = []string{storage.TagResolveExact, storage.TagResolveFuzzy}
)

// jsonObject is a decoded JSON object that remembers its members' input order.
// Pydantic's `input_value` renders a dict in the order the document stated its
// keys — Python dicts preserve insertion order — so the report has to carry that
// order rather than sort it. Go's map does not, hence this type.
type jsonObject struct {
	keys   []string
	values map[string]any
}

// pydanticLoc is one error location: string field names and integer indices.
type pydanticLoc []any

// unionError is one error Pydantic reports, with its location and the three
// rendered lines of its message.
type unionError struct {
	loc     pydanticLoc
	title   string // "Input should be a valid array", "Field required", ...
	kind    string // list_type, missing, string_type, ...
	input   any    // the offending value, rendered with Pydantic's repr
	urlKind string // the fragment after /v/<version>/v/
}

// decodeKnowledgeTagGroups decodes and validates the `tag_groups` parameter the
// way the reference's TypeAdapter does. A document that validates returns the
// groups; anything else returns the rendered detail string, which the caller
// wraps in the reference's 422.
func decodeKnowledgeTagGroups(raw string) ([]knowledgeTagGroup, string, bool) {
	decoded, ok := decodeJSONValue([]byte(raw))
	if !ok {
		return nil, "Invalid tag_groups: " + tagGroupJSONError(raw), false
	}
	errors := validateTagGroupList(decoded)
	if len(errors) > 0 {
		return nil, "Invalid tag_groups: " + renderTagGroupErrors(errors), false
	}
	// The document validated, so it is a list of objects with the members the
	// filter reads; decode it straight into the filter's own shape. (The
	// order-preserving intermediate cannot be marshalled back, and does not need
	// to be: its job was only to carry the input for the report.)
	var groups []knowledgeTagGroup
	if err := json.Unmarshal([]byte(raw), &groups); err != nil {
		return nil, "Invalid tag_groups: " + tagGroupJSONError(raw), false
	}
	return groups, "", true
}

// tagGroupJSONError renders a document the JSON parser itself rejected, in the
// Pydantic `json_invalid` form: the framework parses with a parser whose message
// is CPython's, so the bare text is CPython's and the value is quoted with
// single quotes the way `input_value` renders a string.
func tagGroupJSONError(raw string) string {
	detail := serdeJSONError(raw)
	if detail == "" {
		// The scanner accepted a document the decoder did not; report the
		// parser's opening failure rather than an empty message.
		detail = "expected value at line 1 column 1"
	}
	return renderTagGroupReport([]unionError{{
		title:   "Invalid JSON: " + detail,
		kind:    "json_invalid",
		input:   raw,
		urlKind: "json_invalid",
	}})
}

// validateTagGroupList validates the whole adapter input: a list whose every
// item validates as the TagGroup union. A non-list is a single list_type error
// at the root.
func validateTagGroupList(value any) []unionError {
	items, ok := value.([]any)
	if !ok {
		return []unionError{{
			title:   "Input should be a valid array",
			kind:    "list_type",
			input:   value,
			urlKind: "list_type",
		}}
	}
	errors := make([]unionError, 0)
	for index, item := range items {
		errors = append(errors, validateTagGroupUnion(item, pydanticLoc{json.Number(strconv.Itoa(index))})...)
	}
	return errors
}

// validateTagGroupUnion validates one value against the left-to-right union,
// returning the errors of every member that failed. With union_mode="left_to_right"
// a member that validates ends the union, so a successful member reports nothing
// — but Pydantic still collects the earlier members' failures and reports them
// alongside, which is why a value the *later* members accept can still be
// reported (see the note on the sorting below).
func validateTagGroupUnion(value any, loc pydanticLoc) []unionError {
	failures := make([][]unionError, 0, len(tagGroupUnionMembers))
	for _, member := range tagGroupUnionMembers {
		errors := validateTagGroupMember(value, loc, member)
		if len(errors) == 0 {
			// A member validated. left_to_right keeps the first success, and the
			// reference's handler ignores the report entirely on success (the
			// TypeAdapter call returns without raising), so nothing is reported.
			return nil
		}
		failures = append(failures, errors)
	}
	return interleaveTagGroupErrors(failures)
}

// interleaveTagGroupErrors flattens the per-member failures into the order
// Pydantic reports them for a left-to-right union: the first member's errors
// first, then the second's, and so on. Each error already carries its member's
// name in its location, so the report reads
// "0.TagGroupLeaf.tags" before "0.TagGroupAnd.and".
func interleaveTagGroupErrors(failures [][]unionError) []unionError {
	out := make([]unionError, 0)
	for _, memberErrors := range failures {
		out = append(out, memberErrors...)
	}
	return out
}

// validateTagGroupMember validates one union member: the object must carry the
// member's required field, and that field must validate as the member declares.
// A non-object is a single model_type error, which is all Pydantic reports for
// the member (it never reaches the fields).
func validateTagGroupMember(value any, loc pydanticLoc, member tagGroupUnionMember) []unionError {
	object, ok := value.(*jsonObject)
	if !ok {
		return []unionError{{
			loc:     loc.extended(member.name),
			title:   "Input should be an object",
			kind:    "model_type",
			input:   value,
			urlKind: "model_type",
		}}
	}
	fieldLoc := loc.extended(member.name, member.field)
	field, present := object.values[member.field]
	if !present {
		return []unionError{{
			loc:     fieldLoc,
			title:   "Field required",
			kind:    "missing",
			input:   object,
			urlKind: "missing",
		}}
	}
	if member.list {
		return validateNestedTagGroups(field, fieldLoc)
	}
	if member.field == "not" {
		// `not` is a single nested TagGroup, not a list of them.
		return validateTagGroupUnion(field, fieldLoc)
	}
	return validateTagLeafFields(object, field, fieldLoc)
}

// validateNestedTagGroups validates a compound member's field: a list of
// TagGroups, each of which validates as the union again.
func validateNestedTagGroups(value any, loc pydanticLoc) []unionError {
	items, ok := value.([]any)
	if !ok {
		return []unionError{{
			loc:     loc,
			title:   "Input should be a valid array",
			kind:    "list_type",
			input:   value,
			urlKind: "list_type",
		}}
	}
	errors := make([]unionError, 0)
	for index, item := range items {
		errors = append(errors, validateTagGroupUnion(item, loc.extended(json.Number(strconv.Itoa(index))))...)
	}
	return errors
}

// validateTagLeafFields validates a leaf's `tags`, `match` and `resolve`, in the
// order Pydantic reports them: declaration order, so `tags` before `match`
// before `resolve`.
func validateTagLeafFields(object *jsonObject, tags any, tagsLoc pydanticLoc) []unionError {
	errors := validateTagStringList(tags, tagsLoc)
	if match, present := object.values["match"]; present {
		errors = append(errors, validateTagLeafLiteral(match, tagsLoc.parent().extended("match"),
			tagGroupLeafMatchModes)...)
	}
	if resolve, present := object.values["resolve"]; present {
		errors = append(errors, validateTagLeafLiteral(resolve, tagsLoc.parent().extended("resolve"),
			tagGroupLeafResolveModes)...)
	}
	return errors
}

// validateTagStringList validates a leaf's `tags`: a list whose every member
// coerces to a string. Index positions appear in the location as integers.
func validateTagStringList(value any, loc pydanticLoc) []unionError {
	items, ok := value.([]any)
	if !ok {
		return []unionError{{
			loc:     loc,
			title:   "Input should be a valid array",
			kind:    "list_type",
			input:   value,
			urlKind: "list_type",
		}}
	}
	errors := make([]unionError, 0)
	for index, item := range items {
		if _, isString := item.(string); isString {
			continue
		}
		errors = append(errors, unionError{
			loc:     loc.extended(json.Number(strconv.Itoa(index))),
			title:   "Input should be a valid string",
			kind:    "string_type",
			input:   item,
			urlKind: "string_type",
		})
	}
	return errors
}

// validateTagLeafLiteral validates a Literal member of the leaf, reporting the
// framework's one-or-many wording.
func validateTagLeafLiteral(value any, loc pydanticLoc, expected []string) []unionError {
	if text, ok := value.(string); ok && containsKnowledgeString(expected, text) {
		return nil
	}
	return []unionError{{
		loc:     loc,
		title:   "Input should be " + httpx.ExpectedValues(expected),
		kind:    "literal_error",
		input:   value,
		urlKind: "literal_error",
	}}
}

// extended returns the location with more components appended.
func (l pydanticLoc) extended(parts ...any) pydanticLoc {
	out := make(pydanticLoc, 0, len(l)+len(parts))
	out = append(out, l...)
	return append(out, parts...)
}

// parent returns the location without its last component, which a field
// validator needs to locate a sibling field.
func (l pydanticLoc) parent() pydanticLoc {
	if len(l) == 0 {
		return l
	}
	return l[:len(l)-1]
}

// renderTagGroupErrors renders the full report: the count and title line, then
// one indented block per error.
func renderTagGroupErrors(errors []unionError) string {
	return renderTagGroupReport(errors)
}

// renderTagGroupReport renders a Pydantic validation report the way
// `ValidationError.__str__` does:
//
//	3 validation errors for list[union[...]]
//	0.TagGroupLeaf.tags
//	  Field required [type=missing, input_value={'a': 1}, input_type=dict]
//	    For further information visit https://errors.pydantic.dev/2.12/v/missing
func renderTagGroupReport(errors []unionError) string {
	var b strings.Builder
	noun := "errors"
	if len(errors) == 1 {
		noun = "error"
	}
	fmt.Fprintf(&b, "%d validation %s for %s\n", len(errors), noun, tagGroupUnionTitle)
	for _, item := range errors {
		// A root-level error has an empty location, and Pydantic prints no
		// location line for it at all — the message block follows the header
		// directly.
		if len(item.loc) > 0 {
			b.WriteString(renderPydanticLoc(item.loc))
			b.WriteString("\n")
		}
		b.WriteString("  ")
		b.WriteString(item.title)
		fmt.Fprintf(&b, " [type=%s, input_value=%s, input_type=%s]",
			item.kind, renderPydanticValue(item.input), pydanticTypeName(item.input))
		b.WriteString("\n    For further information visit https://errors.pydantic.dev/2.12/v/")
		b.WriteString(item.urlKind)
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// renderPydanticLoc joins a location path the way Pydantic does: components
// separated by dots, indices as bare integers.
func renderPydanticLoc(loc pydanticLoc) string {
	parts := make([]string, 0, len(loc))
	for _, component := range loc {
		if index, ok := component.(json.Number); ok {
			parts = append(parts, index.String())
			continue
		}
		parts = append(parts, fmt.Sprint(component))
	}
	return strings.Join(parts, ".")
}

// pydanticTypeName names a decoded JSON value's type the way Pydantic's
// `input_type` does.
func pydanticTypeName(value any) string {
	switch typed := value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		if isIntegralJSONNumber(typed) {
			return "int"
		}
		return "float"
	case []any:
		return "list"
	case *jsonObject:
		return "dict"
	}
	return "unknown"
}

// renderPydanticValue renders a value the way Pydantic's `input_value` does:
// Python repr, so strings are single-quoted, dicts use ": " and ", " between
// members with single-quoted keys, and None/True/False spell the Python
// singletons. Object members keep the document's key order, which is how
// Pydantic renders a dict it decoded from the request.
func renderPydanticValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case string:
		return pyReprString(typed)
	case json.Number:
		return typed.String()
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, renderPydanticValue(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *jsonObject:
		parts := make([]string, 0, len(typed.keys))
		for _, key := range typed.keys {
			parts = append(parts, pyReprString(key)+": "+renderPydanticValue(typed.values[key]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(value)
}

// pyReprString renders a string the way Python's repr does, choosing the quote
// Python would: a single quote unless the string holds one and no double quote.
func pyReprString(text string) string {
	quote := '\''
	if strings.ContainsRune(text, '\'') && !strings.ContainsRune(text, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range text {
		switch r {
		case quote, '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// isIntegralJSONNumber reports whether a number carries no fractional part, the
// distinction Pydantic draws between a JSON int and a JSON float.
func isIntegralJSONNumber(number json.Number) bool {
	if _, err := number.Int64(); err == nil {
		return true
	}
	parsed, err := number.Float64()
	return err == nil && parsed == float64(int64(parsed))
}

// decodeJSONValue decodes one JSON document into generic Go values, preserving
// each object's key order (see jsonObject) and keeping a missing document
// distinct from an explicit null.
func decodeJSONValue(raw []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeOrderedValue(decoder)
	if err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return value, true
}

// decodeOrderedValue reads one JSON value from decoder, turning every object
// into a *jsonObject so its key order survives.
func decodeOrderedValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := &jsonObject{values: map[string]any{}}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, fmt.Errorf("object key is not a string")
			}
			value, err := decodeOrderedValue(decoder)
			if err != nil {
				return nil, err
			}
			if _, seen := object.values[key]; !seen {
				object.keys = append(object.keys, key)
			}
			object.values[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		items := make([]any, 0)
		for decoder.More() {
			value, err := decodeOrderedValue(decoder)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return items, nil
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delimiter)
}
