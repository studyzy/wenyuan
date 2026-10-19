package reporting

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file exposes the tag_groups validation report so that every route which
// takes the same `_TAG_GROUPS_ADAPTER` can render the reference's message from
// one implementation rather than several.
//
// The reference renders the same error list two different ways:
//
//   - the knowledge-tree and search routes wrap `str(ValidationError)`
//     (`api/http.py:3715-3719`), which is the multi-line
//     "N validation errors for list[union[...]]" form — RenderString below,
//     and the rendering `decodeKnowledgeTagGroups` has always produced;
//   - the entity and graph routes wrap `{e.errors(include_url=False)}`
//     (`api/http.py:281`), which is the Python repr of a list of error dicts —
//     RenderErrorsRepr below.
//
// Both come from the same parsed error list, so a formatting detail fixed in
// one place cannot drift in the other.

// TagGroupReport is a parsed `tag_groups` failure: the errors Pydantic would
// report, in the order it reports them, with the document they came from.
type TagGroupReport struct {
	errors []unionError
}

// ParseTagGroupsReport validates a `tag_groups` document the way the
// reference's `TypeAdapter(list[TagGroup])` does. It reports the validation
// report when the document is rejected; a document that validates returns a nil
// report and ok=false, because nothing there is a message to render.
func ParseTagGroupsReport(raw string) (report *TagGroupReport, ok bool) {
	decoded, parsed := decodeJSONValue([]byte(raw))
	if !parsed {
		detail := serdeJSONError(raw)
		if detail == "" {
			// The scanner accepted a document the decoder did not; report the
			// parser's opening failure rather than an empty message.
			detail = "expected value at line 1 column 1"
		}
		return &TagGroupReport{errors: []unionError{{
			title:   "Invalid JSON: " + detail,
			kind:    "json_invalid",
			input:   raw,
			urlKind: "json_invalid",
		}}}, true
	}
	errors := validateTagGroupList(decoded)
	if len(errors) == 0 {
		return nil, false
	}
	return &TagGroupReport{errors: errors}, true
}

// RenderString renders the report the way `str(ValidationError)` does — the
// multi-line form the knowledge routes send in their 422 detail. Its output is
// exactly what `decodeKnowledgeTagGroups` has always produced.
func (r *TagGroupReport) RenderString() string { return renderTagGroupReport(r.errors) }

// RenderErrorsRepr renders the report the way `repr(e.errors(include_url=False))`
// does: a Python list of dicts, one per error, in Pydantic's key order
// (`type`, `loc`, `msg`, `input`, then `ctx` when the error carries one).
// `loc` is a Python tuple — `()` for a root error, `(0, 'TagGroupLeaf', 'tags')`
// for a nested one — and `input`/`ctx` use Python's repr.
func (r *TagGroupReport) RenderErrorsRepr() string {
	parts := make([]string, 0, len(r.errors))
	for _, item := range r.errors {
		members := []string{
			"'type': " + renderPyString(item.kind),
			"'loc': " + renderPyLocTuple(item.loc),
			"'msg': " + renderPyString(item.title),
			"'input': " + renderPyInputValue(item.input),
		}
		if ctx := renderPyContext(item); ctx != "" {
			members = append(members, "'ctx': "+ctx)
		}
		parts = append(parts, "{"+strings.Join(members, ", ")+"}")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// renderPyLocTuple renders an error location as a Python tuple. Pydantic's
// `errors()` returns `loc` as a tuple, and a tuple of one is written with a
// trailing comma — `(0,)` — which is the one shape that surprises people.
func renderPyLocTuple(loc pydanticLoc) string {
	if len(loc) == 0 {
		return "()"
	}
	parts := make([]string, 0, len(loc))
	for _, component := range loc {
		if index, isNumber := component.(json.Number); isNumber {
			parts = append(parts, index.String())
			continue
		}
		parts = append(parts, renderPyString(fmt.Sprint(component)))
	}
	if len(parts) == 1 {
		return "(" + parts[0] + ",)"
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// renderPyString renders a string as Python's repr does for the plain ASCII the
// messages carry: single quotes, unless the text holds one and no double quote.
func renderPyString(text string) string { return pyReprString(text) }

// renderPyInputValue renders an error's `input` the way Python's repr does for
// the decoded JSON value.
func renderPyInputValue(value any) string { return renderPydanticValue(value) }

// renderPyContext renders an error's `ctx` dict for the kinds that carry one.
// Pydantic derives it from the error itself, so it is derived here the same way
// rather than stored alongside every error:
//
//   - `json_invalid` carries the parser's message, which is the title without
//     its "Invalid JSON: " prefix;
//   - `literal_error` carries the expected spelling, which is the title without
//     its "Input should be " prefix;
//   - `model_type` carries the model's class name, which is the last named
//     component of the location (the union member Pydantic was trying).
//
// The remaining kinds these members can produce (`missing`, `list_type`,
// `string_type`) carry no context at all.
func renderPyContext(item unionError) string {
	switch item.kind {
	case "json_invalid":
		if message, ok := strings.CutPrefix(item.title, "Invalid JSON: "); ok {
			return "{'error': " + renderPyString(message) + "}"
		}
	case "literal_error":
		if expected, ok := strings.CutPrefix(item.title, "Input should be "); ok {
			return "{'expected': " + renderPyString(expected) + "}"
		}
	case "model_type":
		if className := lastLocationName(item.loc); className != "" {
			return "{'class_name': " + renderPyString(className) + "}"
		}
	}
	return ""
}

// lastLocationName returns the location's last string component, which for a
// `model_type` error is the class Pydantic was validating against.
func lastLocationName(loc pydanticLoc) string {
	for i := len(loc) - 1; i >= 0; i-- {
		if name, ok := loc[i].(string); ok {
			return name
		}
	}
	return ""
}

// TagGroupsDetail renders the complete `Invalid tag_groups: …` detail string a
// route puts in its error body. `reprForm` selects the entity/graph routes'
// `errors()` repr over the knowledge routes' `str(ValidationError)`.
func TagGroupsDetail(report *TagGroupReport, reprForm bool) string {
	if report == nil {
		return "Invalid tag_groups: "
	}
	if reprForm {
		return "Invalid tag_groups: " + report.RenderErrorsRepr()
	}
	return "Invalid tag_groups: " + report.RenderString()
}
