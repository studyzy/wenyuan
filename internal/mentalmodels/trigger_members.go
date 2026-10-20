package mentalmodels

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// The trigger members grouped by the type the reference declares them with, each
// list in the model's declaration order. The order matters because the framework
// reports one member's items before the next one's, whatever order the client
// sent them in.
var (
	// triggerMemberOrder is every declared member, in declaration order.
	triggerMemberOrder = []string{
		"budget", "mode", "refresh_after_consolidation", "refresh_cron",
		"min_refresh_interval_seconds", "fact_types", "exclude_mental_models",
		"exclude_mental_model_ids", "tags_match", "tag_groups", "include_chunks",
		"recall_max_tokens", "recall_chunks_max_tokens",
		"reflect_search_observations_max_tokens",
		"reflect_search_observations_include_entities", "response_schema", "keep_trace",
	}
	// triggerBoolMembers take the framework's lax boolean coercion.
	triggerBoolMembers = []string{
		"refresh_after_consolidation", "exclude_mental_models", "include_chunks",
		"reflect_search_observations_include_entities", "keep_trace",
	}
	// triggerNullableBoolMembers are the boolean members the model declares
	// nullable, so an explicit null clears them instead of being a type error.
	triggerNullableBoolMembers = []string{"include_chunks", "reflect_search_observations_include_entities"}
	// triggerIntMembers are integers with an inclusive lower bound: 1 for the
	// token budgets, 0 for the refresh interval.
	triggerIntMembers = []string{
		"min_refresh_interval_seconds", "recall_max_tokens", "recall_chunks_max_tokens",
		"reflect_search_observations_max_tokens",
	}
)

// triggerValidator is one of the model's own (custom) validators: it reports an
// item when it refuses the member's value.
type triggerValidator struct {
	member   string
	validate func(object map[string]any, raw any) (httpx.ValidationError, bool)
}

// triggerCustomValidators are the reference's custom validators in declaration
// order, one per member that declares one. The model-level exclusivity check is
// not in this list: it is run separately, after every field-level check passed,
// because the framework skips model-level validators once any field has failed.
var triggerCustomValidators = []triggerValidator{
	{"fact_types", triggerFactTypesValidator},
	{"tag_groups", triggerTagGroupsValidator},
	{"response_schema", triggerResponseSchemaValidator},
}

// triggerFactTypesValidator refuses an empty fact-type list: null means every
// type, and an empty list would read as "no type at all".
func triggerFactTypesValidator(_ map[string]any, raw any) (httpx.ValidationError, bool) {
	factTypes, isList := raw.([]any)
	if !isList || len(factTypes) > 0 {
		return httpx.ValidationError{}, false
	}
	return httpx.ValueError(triggerLoc("fact_types"), raw,
		"fact_types must not be empty. Use null to include all fact types."), true
}

// triggerTagGroupsValidator refuses a fuzzy leaf. A trigger's scope is read by
// two paths that resolve differently: a refresh resolves fuzzy leaves while the
// staleness check builds SQL straight from the stored groups, so a stored fuzzy
// leaf would never be marked stale by what it matched.
func triggerTagGroupsValidator(_ map[string]any, raw any) (httpx.ValidationError, bool) {
	groups, isList := raw.([]any)
	if !isList || !triggerHasFuzzy(groups) {
		return httpx.ValidationError{}, false
	}
	return httpx.ValueError(triggerLoc("tag_groups"), raw,
		"resolve='fuzzy' is not supported in a trigger's tag_groups. A stored scope must be "+
			"exact; use it on a recall or reflect request instead."), true
}

// triggerResponseSchemaValidator refuses a schema the structured-output
// extractor could not build a model from.
func triggerResponseSchemaValidator(_ map[string]any, raw any) (httpx.ValidationError, bool) {
	schema, isObject := raw.(map[string]any)
	if !isObject {
		return httpx.ValidationError{}, false
	}
	if message := responseSchemaProblem(schema); message != "" {
		return httpx.ValueError(triggerLoc("response_schema"), raw, message), true
	}
	return httpx.ValidationError{}, false
}

// triggerExclusivityValidator refuses a trigger that schedules both kinds of
// refresh. It is the model-level check, so its item names the whole trigger
// object as input and runs after every field-level validator.
func triggerExclusivityValidator(object map[string]any, _ any) (httpx.ValidationError, bool) {
	refreshAfter, isBool := object["refresh_after_consolidation"].(bool)
	expression, isString := object["refresh_cron"].(string)
	if !isBool || !refreshAfter || !isString || strings.TrimSpace(expression) == "" {
		return httpx.ValidationError{}, false
	}
	return httpx.ValueError([]string{"body", "trigger"}, object,
		"refresh_after_consolidation and refresh_cron are mutually exclusive: "+
			"a mental model refreshes either after consolidation or on a cron schedule, not both."), true
}

// triggerHasFuzzy reports whether a tag-group tree carries a fuzzy resolution,
// which a stored trigger scope may not.
func triggerHasFuzzy(value any) bool {
	switch node := value.(type) {
	case []any:
		for _, entry := range node {
			if triggerHasFuzzy(entry) {
				return true
			}
		}
	case map[string]any:
		if resolve, ok := node["resolve"].(string); ok && strings.EqualFold(resolve, "fuzzy") {
			return true
		}
		for _, key := range []string{"and", "or", "not"} {
			if triggerHasFuzzy(node[key]) {
				return true
			}
		}
	}
	return false
}

// The tag-group union's branch names, in the order the reference tries them: a
// leaf is identified by its tags, and each compound group by its own key.
const (
	tagGroupLeaf = "TagGroupLeaf"
	tagGroupAnd  = "TagGroupAnd"
	tagGroupOr   = "TagGroupOr"
	tagGroupNot  = "TagGroupNot"
)

// collectTagGroupItems reports the items one tag-group node violates. The
// reference declares the tree as a discriminated union, and a union reports the
// failure of EVERY alternative when none of them takes the value, each under the
// alternative's own name; a value that fits one alternative reports nothing.
// That shape is reproduced here, recursively for a compound group's children.
func collectTagGroupItems(value any, loc []string, items *[]httpx.ValidationError) {
	if tagGroupFitsAny(value) {
		return
	}
	for _, branch := range []string{tagGroupLeaf, tagGroupAnd, tagGroupOr, tagGroupNot} {
		collectTagGroupBranch(value, branch, loc, items)
	}
}

// tagGroupFitsAny reports whether one alternative takes the value — the union's
// own question. An alternative takes it when the member it requires has the
// right kind AND every optional member it declares is one of its accepted
// values: the leaf's match and resolve are Literals, so an unknown mode keeps
// the leaf from taking the value even though its tags are a list.
func tagGroupFitsAny(value any) bool {
	group, isObject := value.(map[string]any)
	if !isObject {
		return false
	}
	for _, branch := range []string{tagGroupLeaf, tagGroupAnd, tagGroupOr, tagGroupNot} {
		if tagGroupShapeProblem(group, branch) != "" {
			continue
		}
		if branch == tagGroupLeaf && !tagLeafMembersAccepted(group) {
			continue
		}
		return true
	}
	return false
}

// tagLeafMembersAccepted reports whether a leaf's optional match and resolve
// members hold values from their Literals.
func tagLeafMembersAccepted(group map[string]any) bool {
	if raw, present := group["match"]; present {
		match, isString := raw.(string)
		if !isString || !contains(tagLeafMatchValues, match) {
			return false
		}
	}
	if raw, present := group["resolve"]; present {
		resolve, isString := raw.(string)
		if !isString || !contains(tagGroupResolutions, resolve) {
			return false
		}
	}
	return true
}

// collectTagGroupBranch reports what one alternative says about the value.
func collectTagGroupBranch(value any, branch string, loc []string, items *[]httpx.ValidationError) {
	branchLoc := appendMember(loc, branch)
	group, isObject := value.(map[string]any)
	if !isObject {
		*items = append(*items, httpx.TypeMismatch("model_attributes_type", branchLoc, value))
		return
	}
	if problem := tagGroupShapeProblem(group, branch); problem != "" {
		*items = append(*items, httpValidationItem(problem, branchLoc, group, branch))
		return
	}
	switch branch {
	case tagGroupLeaf:
		if raw, present := group["match"]; present {
			match, isString := raw.(string)
			if !isString || !contains(tagLeafMatchValues, match) {
				*items = append(*items, httpx.Literal(appendMember(branchLoc, "match"), raw, tagLeafMatchValues))
			}
		}
		if raw, present := group["resolve"]; present {
			resolve, isString := raw.(string)
			if !isString || !contains(tagGroupResolutions, resolve) {
				*items = append(*items, httpx.Literal(appendMember(branchLoc, "resolve"), raw, tagGroupResolutions))
			}
		}
	case tagGroupAnd, tagGroupOr:
		key := "and"
		if branch == tagGroupOr {
			key = "or"
		}
		children, _ := tagGroupMember(group, key, "filters")
		list, _ := children.([]any)
		for index, child := range list {
			collectTagGroupItems(child, appendMember(appendMember(branchLoc, key), fmt.Sprint(index)), items)
		}
	case tagGroupNot:
		child, _ := tagGroupMember(group, "not", "filter")
		collectTagGroupItems(child, appendMember(branchLoc, "not"), items)
	}
}

// tagGroupResolutions are the values a leaf's resolve member accepts, and
// tagLeafMatchValues the modes its match member does — the full tags-match
// vocabulary, not only the subset a request-level tags_match accepts.
var (
	tagGroupResolutions = []string{"exact", "fuzzy"}
	tagLeafMatchValues  = []string{"any", "all", "any_strict", "all_strict", "exact"}
)

// tagGroupShapeProblem returns the framework's item type for the member the
// alternative requires when it is absent or of the wrong kind, or "" when the
// alternative takes the value. A compound group declares its children under a
// field the wire spells with an alias (`and`, `or`, `not`) and accepts either
// spelling, so a group written with the field name is the same group.
func tagGroupShapeProblem(group map[string]any, branch string) string {
	switch branch {
	case tagGroupLeaf:
		raw, present := group["tags"]
		if !present {
			return "missing"
		}
		if _, isList := raw.([]any); !isList {
			return "list_type"
		}
	case tagGroupAnd:
		if raw, present := tagGroupMember(group, "and", "filters"); !present || !isList(raw) {
			return keyShape(present, "list_type")
		}
	case tagGroupOr:
		if raw, present := tagGroupMember(group, "or", "filters"); !present || !isList(raw) {
			return keyShape(present, "list_type")
		}
	case tagGroupNot:
		if raw, present := tagGroupMember(group, "not", "filter"); !present || !isObject(raw) {
			return keyShape(present, "model_attributes_type")
		}
	}
	return ""
}

// tagGroupMember reads a compound group's child member under either its wire
// alias or its field name.
func tagGroupMember(group map[string]any, alias, name string) (any, bool) {
	if raw, present := group[alias]; present {
		return raw, true
	}
	raw, present := group[name]
	return raw, present
}

// keyShape names an absent member as missing and one present with the wrong kind
// as that kind's type failure.
func keyShape(present bool, wrongKind string) string {
	if present {
		return wrongKind
	}
	return "missing"
}

// isList and isObject are the two JSON kinds the union's members declare.
func isList(value any) bool   { _, ok := value.([]any); return ok }
func isObject(value any) bool { _, ok := value.(map[string]any); return ok }

// httpValidationItem builds the item for a shape problem: the framework's missing
// item carries the whole node as input, and a wrong kind names the value it
// refused.
func httpValidationItem(problem string, loc []string, group map[string]any, branch string) httpx.ValidationError {
	var member any
	switch branch {
	case tagGroupAnd:
		member, _ = tagGroupMember(group, "and", "filters")
	case tagGroupOr:
		member, _ = tagGroupMember(group, "or", "filters")
	case tagGroupNot:
		member, _ = tagGroupMember(group, "not", "filter")
	default:
		member = group["tags"]
	}
	if problem == "missing" {
		// The member the alternative requires is absent, so its item names that
		// member and carries the whole node as input.
		return httpx.Missing(appendMember(loc, tagGroupKey(branch)), group)
	}
	return httpx.TypeMismatch(problem, loc, member)
}

// tagGroupKey is the member name each alternative requires, as the wire spells
// it.
func tagGroupKey(branch string) string {
	switch branch {
	case tagGroupAnd:
		return "and"
	case tagGroupOr:
		return "or"
	case tagGroupNot:
		return "not"
	default:
		return "tags"
	}
}

// appendMember extends a location with one member name or index.
func appendMember(loc []string, member string) []string {
	out := make([]string, len(loc), len(loc)+1)
	copy(out, loc)
	return append(out, member)
}

// triggerBoolItem is the framework's item for a boolean member it could not read.
// The two kinds it distinguishes come from where the refusal happened: a string
// and a number get as far as the lax conversion's word/number reading and fail
// *parsing*, every other kind never does and is the wrong *type*.
func triggerBoolItem(member string, raw any) httpx.ValidationError {
	switch typed := raw.(type) {
	case string:
		return httpx.ScalarTypeMismatch("bool", triggerLoc(member), typed)
	case float64:
		// An integer outside 0/1 is a *parsing* failure — the framework reads the
		// numeric spelling before giving up — while a number carrying a fraction
		// is the plain type mismatch.
		if typed == math.Trunc(typed) {
			return httpx.TypeMismatch("bool_parsing", triggerLoc(member), typed)
		}
	}
	return httpx.TypeMismatch("bool_type", triggerLoc(member), raw)
}

// The property types the structured-output extractor can build a field for.
var responseSchemaTypes = []string{"string", "number", "integer", "boolean", "array", "object"}

// responseSchemaProblem reports the first reason a supplied response schema is
// not one the extractor can use, or "" when it is usable. The extractor builds a
// flat model from the schema's top-level properties, so the contract is checked
// at the API boundary rather than failing later inside the model call.
func responseSchemaProblem(schema map[string]any) string {
	if schemaType, present := schema["type"]; present && schemaType != nil {
		if text, isString := schemaType.(string); !isString || text != "object" {
			return `response_schema must be an object schema (its "type" must be "object")`
		}
	}
	properties, isObject := schema["properties"].(map[string]any)
	if !isObject || len(properties) == 0 {
		return "response_schema must define a non-empty 'properties' object"
	}
	// The reference builds its model from the properties in declaration order,
	// so the first unusable entry is the one reported.
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		property, isObject := properties[name].(map[string]any)
		if !isObject {
			return fmt.Sprintf("response_schema property '%s' must be an object", name)
		}
		propertyType, present := property["type"]
		if !present || propertyType == nil {
			continue
		}
		text, isString := propertyType.(string)
		if !isString || !contains(responseSchemaTypes, text) {
			return fmt.Sprintf("response_schema property '%s' has unsupported type '%v'; expected one of %s",
				name, propertyType, renderedTypes(responseSchemaTypes))
		}
	}
	if required, present := schema["required"]; present && required != nil {
		names, isList := required.([]any)
		if !isList {
			return "response_schema 'required' must be a list of property names"
		}
		unknown := make([]string, 0)
		for _, entry := range names {
			text, isString := entry.(string)
			if !isString {
				return "response_schema 'required' must be a list of property names"
			}
			if _, declared := properties[text]; !declared {
				unknown = append(unknown, "'"+text+"'")
			}
		}
		if len(unknown) > 0 {
			return "response_schema 'required' references unknown properties: [" + strings.Join(unknown, ", ") + "]"
		}
	}
	return ""
}

// renderedTypes renders a type list the way the reference's message does: a
// sorted list in the framework's repr form.
func renderedTypes(values []string) string {
	sorted := make([]string, len(values))
	copy(sorted, values)
	sort.Strings(sorted)
	quoted := make([]string, len(sorted))
	for i, value := range sorted {
		quoted[i] = "'" + value + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
