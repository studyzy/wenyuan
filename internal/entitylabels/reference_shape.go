package entitylabels

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file reproduces how the reference validates a bank's entity_labels
// config, and how it reports a rejected one.
//
// The reference hands the raw value to `parse_entity_labels`
// (engine/retain/entity_labels.py:37-85), which validates every group against
// its pydantic model `LabelGroup` (:27-35) and every map field against
// `MapField` (:19-24). A rejection surfaces as `pydantic.ValidationError`, and
// the config write wraps it verbatim:
//
//	raise ValueError(f"Invalid entity_labels format: {e}")   # config_resolver.py:601-607
//
// so the 400 body is the model library's own multi-line report. Two contract
// facts follow from that, and both are observable on the wire:
//
//   - which values a group may carry. The parser is the only gate, so a group
//     whose `optional` or `description` is the wrong type is rejected here even
//     though nothing downstream reads those members.
//   - what the report says. It names each offending member by its pydantic
//     path (`values.0.value`) in declaration order, with a `[type=..., input_value=...,
//     input_type=...]` suffix and a documentation link.
//
// The internal vocabulary (vocabulary.go) keeps its own coercion rules, so this
// shape check runs as an additional gate beside it rather than replacing it.

// labelGroupTypes and mapFieldTypes are the members of the two Literal types,
// in the order the reference declares them (its enum message lists them so).
var (
	labelGroupTypes = []string{TypeValue, TypeMultiValues, TypeText, TypeMultiText, TypeMap}
	mapFieldTypes   = []string{TypeText, TypeMultiText, TypeValue, TypeMultiValues, TypeMap}
)

// labelValue is one entry of a `values` list, kept alongside its member keys so
// the reference's re-serialisation order can be reproduced.
type labelValue struct {
	value       string
	description any
}

// labelGroup is one validated group.
type labelGroup struct {
	key         string
	description any
	groupType   string
	// groupTypeSupplied records whether the caller named a type; the migration
	// supplies one for a legacy group, which is not written back.
	groupTypeSupplied bool
	optional          bool
	tag               bool
	values            []labelValue
	fields            map[string]*mapField

	// fieldsIsObject marks a `fields` member that is a JSON object, which is
	// the only shape whose entries are validated as MapFields.
	fieldsIsObject bool
}

// mapField is one validated map field, held as an ordered key list plus a map
// so both validation and rendering iterate in the caller's order.
type mapField struct {
	typeName    string
	description any
	values      []labelValue
	fields      map[string]*mapField

	fieldsObject bool
}

// ShapeProblems returns the reference's validation report for a raw
// entity_labels value, or nil when the reference's model accepts it.
//
// The report is rendered the way `str(ValidationError)` renders it, including
// the two-space indent of each message, the trailing document link line, and
// the "N validation errors for LabelGroup" headline. A single problem uses the
// singular headline the library uses.
func ShapeProblems(raw any) []string {
	parsed, ok := Normalize(raw)
	if !ok || parsed == nil {
		return nil
	}
	groups, _ := parsed.([]any)
	var problems []problem
	// The reference validates the group list with a comprehension
	// (engine/retain/entity_labels.py:78), so the first group its model rejects
	// ends validation: a later group's problems are never reported.
	for _, entry := range groups {
		object, isObject := entry.(map[string]any)
		if !isObject {
			problems = append(problems, problem{
				loc:   "",
				kind:  "model_type",
				msg:   "Input should be a valid dictionary or instance of LabelGroup",
				input: entry,
			})
			break
		}
		_, groupProblems := checkGroup(migrate(object))
		if len(groupProblems) > 0 {
			problems = append(problems, groupProblems...)
			break
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return renderProblems("LabelGroup", problems)
}

// Normalize exposes the config shapes Parse reads, so a caller can inspect the
// group list the reference would validate.
func Normalize(raw any) (any, bool) {
	normalised, err := normalise(raw)
	if err != nil {
		return nil, false
	}
	switch typed := normalised.(type) {
	case []any:
		return typed, true
	case map[string]any:
		attributes, present := typed["attributes"]
		if !present || attributes == nil {
			return nil, true
		}
		list, ok := attributes.([]any)
		if !ok {
			return nil, false
		}
		return list, true
	}
	return nil, true
}

// Canonical returns the value in the shape the reference's serialiser writes:
// its models' declaration order at every level, and only the keys those models
// declare. A client comparing our bank config against the reference's sees the
// same bytes once the object is rendered.
func Canonical(raw any) any {
	parsed, ok := Normalize(raw)
	if !ok || parsed == nil {
		return nil
	}
	groups, _ := parsed.([]any)
	if len(groups) == 0 {
		// An empty list is still the envelope the model serialises: the caller
		// supplied an (empty) attributes list, so the stored value keeps one.
		return OrderedObject{Keys: []string{"attributes"}, Members: map[string]any{"attributes": []any{}}}
	}
	out := make([]any, 0, len(groups))
	for _, entry := range groups {
		object, isObject := entry.(map[string]any)
		if !isObject {
			continue
		}
		// Whether the caller named a type decides whether the stored value
		// carries one: the legacy migration supplies it only to validate.
		_, callerNamedType := object["type"]
		group, problems := checkGroup(migrate(object))
		if group == nil || len(problems) > 0 {
			continue
		}
		group.groupTypeSupplied = callerNamedType
		out = append(out, group.render())
	}
	return OrderedObject{Keys: []string{"attributes"}, Members: map[string]any{"attributes": out}}
}

// checkGroup validates one group the way LabelGroup.model_validate does,
// reporting each member's problem at its pydantic path.
func checkGroup(raw map[string]any) (*labelGroup, []problem) {
	var problems []problem
	group := &labelGroup{}

	if value, present := raw["key"]; !present {
		problems = append(problems, problemWithShown("key", "missing", "Field required", raw))
	} else if text, ok := value.(string); ok {
		group.key = text
	} else {
		problems = append(problems, problem{loc: "key", kind: "string_type", msg: "Input should be a valid string", input: value})
	}

	group.description = raw["description"]
	if value, present := raw["description"]; present && value != nil {
		if _, ok := value.(string); !ok {
			problems = append(problems, problem{loc: "description", kind: "string_type", msg: "Input should be a valid string", input: value})
		}
	}

	// The legacy migration always supplies a `type`, so the member is only
	// wrong when the caller gave something outside the five names — including a
	// `null`, which the model reports as a literal mismatch rather than a type
	// error.
	if value, present := raw["type"]; present {
		text, isText := value.(string)
		if isText && containsString(labelGroupTypes, text) {
			group.groupType = text
			group.groupTypeSupplied = true
		} else {
			problems = append(problems, problem{loc: "type", kind: "literal_error",
				msg:   "Input should be " + ExpectedValues(labelGroupTypes),
				input: value})
		}
	} else {
		group.groupType = TypeValue
	}

	group.optional = true
	if value, present := raw["optional"]; present && value != nil {
		flag, ok, kind, msg := laxBoolean(value)
		if !ok {
			problems = append(problems, problem{loc: "optional", kind: kind, msg: msg, input: value})
		} else {
			group.optional = flag
		}
	}

	if value, present := raw["tag"]; present && value != nil {
		flag, ok, kind, msg := laxBoolean(value)
		if !ok {
			problems = append(problems, problem{loc: "tag", kind: kind, msg: msg, input: value})
		} else {
			group.tag = flag
		}
	}

	values, valueProblems := checkValuesMember("values", raw, "values")
	group.values = values
	problems = append(problems, valueProblems...)

	if fields, present := raw["fields"]; present && fields != nil {
		object, ok := fields.(map[string]any)
		if !ok {
			problems = append(problems, problem{loc: "fields", kind: "dict_type",
				msg: "Input should be a valid dictionary", input: fields})
		} else {
			group.fieldsIsObject = true
			checked, fieldProblems := checkFields("fields", object)
			group.fields = checked
			problems = append(problems, fieldProblems...)
		}
	}

	return group, problems
}

// checkFields validates a `fields` object's entries as MapFields.
func checkFields(prefix string, raw map[string]any) (map[string]*mapField, []problem) {
	var problems []problem
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	// pydantic reports a dict's members in the order they arrived; a decoded
	// JSON object keeps its order in Python, and encoding/json does not, so
	// the closest reproducible order is the sorted one.
	sort.Strings(names)
	out := make(map[string]*mapField, len(names))
	for _, name := range names {
		entry := raw[name]
		object, ok := entry.(map[string]any)
		if !ok {
			problems = append(problems, problem{loc: prefix + "." + name, kind: "model_type",
				msg: "Input should be a valid dictionary or instance of MapField", input: entry})
			continue
		}
		field, fieldProblems := checkField(prefix+"."+name, object)
		out[name] = field
		problems = append(problems, fieldProblems...)
	}
	return out, problems
}

// checkField validates one MapField.
func checkField(prefix string, raw map[string]any) (*mapField, []problem) {
	var problems []problem
	field := &mapField{typeName: TypeText}

	if value, present := raw["type"]; present {
		if text, ok := value.(string); ok {
			if containsString(mapFieldTypes, text) {
				field.typeName = text
			} else {
				problems = append(problems, problem{loc: prefix + ".type", kind: "literal_error",
					msg:   "Input should be " + ExpectedValues(mapFieldTypes),
					input: value})
			}
		} else {
			problems = append(problems, problem{loc: prefix + ".type", kind: "string_type",
				msg: "Input should be a valid string", input: value})
		}
	}

	field.description = raw["description"]
	if value, present := raw["description"]; present && value != nil {
		if _, ok := value.(string); !ok {
			problems = append(problems, problem{loc: prefix + ".description", kind: "string_type",
				msg: "Input should be a valid string", input: value})
		}
	}

	values, valueProblems := checkValuesMember(prefix+".values", raw, "values")
	field.values = values
	problems = append(problems, valueProblems...)

	if fields, present := raw["fields"]; present && fields != nil {
		object, ok := fields.(map[string]any)
		if !ok {
			problems = append(problems, problem{loc: prefix + ".fields", kind: "dict_type",
				msg: "Input should be a valid dictionary", input: fields})
		} else {
			field.fieldsObject = true
			checked, fieldProblems := checkFields(prefix+".fields", object)
			field.fields = checked
			problems = append(problems, fieldProblems...)
		}
	}
	return field, problems
}

// checkValues validates a `values` list of LabelValue objects.
func checkValues(prefix string, raw any) ([]labelValue, []problem) {
	list, ok := raw.([]any)
	if !ok {
		return nil, []problem{{loc: prefix, kind: "list_type",
			msg: "Input should be a valid list", input: raw}}
	}
	var problems []problem
	out := make([]labelValue, 0, len(list))
	for index, entry := range list {
		loc := fmt.Sprintf("%s.%d", prefix, index)
		object, ok := entry.(map[string]any)
		if !ok {
			problems = append(problems, problem{loc: loc, kind: "model_type",
				msg: "Input should be a valid dictionary or instance of LabelValue", input: entry})
			continue
		}
		value := labelValue{}
		if text, present := object["value"]; !present {
			problems = append(problems, problem{loc: loc + ".value", kind: "missing", msg: "Field required", input: object})
		} else if text2, ok := text.(string); ok {
			value.value = text2
		} else {
			problems = append(problems, problem{loc: loc + ".value", kind: "string_type",
				msg: "Input should be a valid string", input: text})
		}
		value.description = object["description"]
		if description, present := object["description"]; present && description != nil {
			if _, ok := description.(string); !ok {
				problems = append(problems, problem{loc: loc + ".description", kind: "string_type",
					msg: "Input should be a valid string", input: description})
			}
		}
		out = append(out, value)
	}
	return out, problems
}

// laxBoolean applies the model library's boolean coercion: a JSON boolean is
// taken as is, the accepted words are parsed, and 0/1 integers convert. A
// member that is absent or null uses the field's default and is not checked
// here (a required boolean member reports a type error through the caller).
func laxBoolean(value any) (bool, bool, string, string) {
	switch typed := value.(type) {
	case bool:
		return typed, true, "", ""
	case string:
		switch strings.ToLower(typed) {
		case "true", "t", "yes", "y", "on", "1":
			return true, true, "", ""
		case "false", "f", "no", "n", "off", "0":
			return false, true, "", ""
		}
		return false, false, "bool_parsing", "Input should be a valid boolean, unable to interpret input"
	case float64:
		if typed == 0 {
			return false, true, "", ""
		}
		if typed == 1 {
			return true, true, "", ""
		}
	}
	return false, false, "bool_parsing", "Input should be a valid boolean, unable to interpret input"
}

// problem is one entry of the reference's validation report.
type problem struct {
	loc   string
	kind  string
	msg   string
	input any
	// shown, when set, lists the keys an object's repr renders, in order.
	shown []string
}

// ExpectedValues renders a Literal's members the way the model library words
// them: single-quoted and joined with "or" before the last member.
func ExpectedValues(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + value + "'"
	}
	switch len(quoted) {
	case 0:
		return ""
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

// renderProblems renders the model library's multi-line report. The headline
// names the model and the count, each problem is a header line (the pydantic
// location) followed by a two-space indented message with the type, input value
// and input type in brackets, and each closes with the documentation link line.
func renderProblems(model string, problems []problem) []string {
	var b strings.Builder
	if len(problems) == 1 {
		fmt.Fprintf(&b, "1 validation error for %s\n", model)
	} else {
		fmt.Fprintf(&b, "%d validation errors for %s\n", len(problems), model)
	}
	for _, p := range problems {
		// A problem filed at the model itself (a group that is not an object)
		// has no location, and the library omits the header line entirely.
		if p.loc != "" {
			b.WriteString(p.loc)
			b.WriteByte('\n')
		}
		shown := p.input
		fmt.Fprintf(&b, "  %s [type=%s, input_value=%s, input_type=%s]\n",
			p.msg, p.kind, pythonReprOrdered(shown, p.shown), PythonType(p.input))
		fmt.Fprintf(&b, "    For further information visit https://errors.pydantic.dev/2.12/v/%s\n", p.kind)
	}
	return strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
}

// PythonType names a decoded JSON value's Python type, which the report's
// input_type field carries.
func PythonType(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case float64, int:
		return "int"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	}
	return "object"
}

// PythonRepr renders a value the way the model library's report does. This is
// the same rendering the config write's other messages already use, kept local
// to this package so it stays dependency-free.
func PythonRepr(value any) string { return pythonReprOrdered(value, nil) }

// pythonReprOrdered renders a value, listing an object's members in the given
// order (any member it does not name follows, sorted).
func pythonReprOrdered(value any, order []string) string {
	switch typed := value.(type) {
	case map[string]any:
		keys := orderedKeys(typed, order)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, pythonStringRepr(key)+": "+pythonReprOrdered(typed[key], nil))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []any:
		parts := make([]string, len(typed))
		for i, item := range typed {
			parts[i] = pythonReprOrdered(item, nil)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return pythonReprOrderedScalar(value)
}

// orderedKeys lists an object's keys: those named in order first, then the rest
// sorted.
func orderedKeys(object map[string]any, order []string) []string {
	keys := make([]string, 0, len(object))
	seen := make(map[string]bool, len(order))
	for _, key := range order {
		if _, present := object[key]; present && !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	rest := make([]string, 0, len(object))
	for key := range object {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// pythonReprOrderedScalar is PythonRepr for a value with no nested order.
func pythonReprOrderedScalar(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case string:
		return pythonStringRepr(typed)
	case float64:
		return pythonFloatRepr(typed)
	case int:
		return strconv.Itoa(typed)
	}
	return fmt.Sprint(value)
}

func pythonStringRepr(text string) string {
	if strings.Contains(text, "'") && !strings.Contains(text, "\"") {
		return `"` + text + `"`
	}
	return "'" + strings.ReplaceAll(text, "'", `\'`) + "'"
}

func pythonFloatRepr(number float64) string {
	if number == float64(int64(number)) && number < 1e16 && number > -1e16 {
		return strconv.FormatInt(int64(number), 10)
	}
	return strconv.FormatFloat(number, 'g', -1, 64)
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// The stored value is rendered from plain maps because it round-trips through
// the bank's jsonb column, which cannot carry a Go type's field order. The order
// the reference's serialiser produces — each model's declaration order — is
// applied on the way out by Marshal, which the config write calls when it stores
// a parsed vocabulary.
//
//	A group:   key, description, type, optional, tag, values, fields
//	A field:   type, description, values, fields
//	A value:   value, description
// The envelope is {"attributes": [...]}.

// The shape names ordered() recognises.
const (
	groupShape = "group"
	fieldShape = "field"
	valueShape = "value"
)

// groupMemberOrder is LabelGroup's declaration order.
var groupMemberOrder = []string{"key", "description", "type", "optional", "tag", "values", "fields"}

// fieldMemberOrder is MapField's declaration order.
var fieldMemberOrder = []string{"type", "description", "values", "fields"}

// valueMemberOrder is LabelValue's declaration order.
var valueMemberOrder = []string{"value", "description"}

// Marshal renders a canonical vocabulary value in the reference's serialiser
// order, so the bytes stored (and therefore returned) match.
func Marshal(value any) ([]byte, error) {
	return marshalOrdered(value, "")
}

// marshalOrdered renders value, ordering objects by the member order of the
// shape their path names ("" is the envelope, "group", "field", "value").
func marshalOrdered(value any, shape string) ([]byte, error) {
	switch typed := value.(type) {
	case map[string]any:
		var order []string
		childShape := shape
		switch shape {
		case "":
			order = []string{"attributes"}
			childShape = "group"
		case "group":
			order = groupMemberOrder
		case "field":
			order = fieldMemberOrder
		case "value":
			order = valueMemberOrder
		}
		if shape == "group" {
			childShape = "member"
		}
		var buf bytes.Buffer
		buf.WriteByte('{')
		for i, key := range orderedKeys(typed, order) {
			if i > 0 {
				buf.WriteByte(',')
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return nil, err
			}
			buf.Write(encodedKey)
			buf.WriteByte(':')
			next := childShape
			switch key {
			case "attributes":
				next = "group"
			case "values":
				next = "value"
			case "fields":
				next = "field"
			}
			if shape == "member" {
				next = "group"
			}
			encoded, err := marshalOrdered(typed[key], next)
			if err != nil {
				return nil, err
			}
			buf.Write(encoded)
		}
		buf.WriteByte('}')
		return buf.Bytes(), nil
	case []any:
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				buf.WriteByte(',')
			}
			encoded, err := marshalOrdered(item, shape)
			if err != nil {
				return nil, err
			}
			buf.Write(encoded)
		}
		buf.WriteByte(']')
		return buf.Bytes(), nil
	}
	return json.Marshal(value)
}

// render emits a validated group.
func (g *labelGroup) render() OrderedObject {
	out := map[string]any{"key": g.key}
	// The reference stores the caller's own dict after validating it, so a
	// `type` the legacy migration supplied is not written back: only one the
	// caller gave appears. The same holds for every member below.
	if g.groupTypeSupplied {
		out["type"] = g.groupType
	}
	// The model serialises a member the caller set to a non-default value:
	// `optional` defaults to true and `tag` to false.
	if value, present := g.description.(string); present && value != "" {
		out["description"] = value
	}
	if !g.optional {
		out["optional"] = false
	}
	if g.tag {
		out["tag"] = true
	}
	if len(g.values) > 0 {
		out["values"] = g.renderValues()
	}
	if g.fieldsIsObject && len(g.fields) > 0 {
		out["fields"] = renderFields(g.fields)
	}
	return ordered(groupShape, out)
}

func (g *labelGroup) renderValues() []any {
	out := make([]any, 0, len(g.values))
	for _, value := range g.values {
		out = append(out, value.render())
	}
	return out
}

// render emits a validated map field as a plain map.
func (f *mapField) render() OrderedObject {
	out := map[string]any{"type": f.typeName}
	if value, present := f.description.(string); present && value != "" {
		out["description"] = value
	}
	if len(f.values) > 0 {
		values := make([]any, 0, len(f.values))
		for _, value := range f.values {
			values = append(values, value.render())
		}
		out["values"] = values
	}
	if f.fieldsObject && len(f.fields) > 0 {
		out["fields"] = renderFields(f.fields)
	}
	return ordered(fieldShape, out)
}

func (v labelValue) render() OrderedObject {
	out := map[string]any{"value": v.value}
	if description, present := v.description.(string); present && description != "" {
		out["description"] = description
	}
	return ordered(valueShape, out)
}

func renderFields(fields map[string]*mapField) map[string]any {
	out := make(map[string]any, len(fields))
	for name, field := range fields {
		out[name] = field.render()
	}
	return out
}

// ordered wraps a rendered object in its shape's member order, so whichever
// serialiser writes it (the config store's or a response's) lists the members
// the way the reference's own serialiser does.
func ordered(shape string, members map[string]any) OrderedObject {
	var keys []string
	switch shape {
	case "group":
		keys = groupMemberOrder
	case "field":
		keys = fieldMemberOrder
	case "value":
		keys = valueMemberOrder
	}
	return OrderedObject{Keys: keys, Members: members}
}

// problemWithShown builds a missing-member item whose printed input is the
// migrated group dict, listed in the order that dict holds its keys.
func problemWithShown(loc, kind, msg string, migrated map[string]any) problem {
	return problem{loc: loc, kind: kind, msg: msg, input: migrated, shown: migrationDictOrder(migrated)}
}

// migrationDictOrder lists a migrated group's members the way the reference's
// dict holds them: the caller's own keys in the order a Go decode can recover
// (sorted), then `type`, which the migration appends when the caller omitted it.
func migrationDictOrder(migrated map[string]any) []string {
	keys := make([]string, 0, len(migrated))
	for key := range migrated {
		if key != "type" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if _, present := migrated["type"]; present {
		keys = append(keys, "type")
	}
	return keys
}

// checkValuesMember validates a `values` member, telling an absent one (which
// takes the field's default) from an explicit null (which the model reports as
// a list mismatch).
func checkValuesMember(prefix string, raw map[string]any, member string) ([]labelValue, []problem) {
	value, present := raw[member]
	if !present {
		return nil, nil
	}
	return checkValues(prefix, value)
}

// OrderedObject is a JSON object whose members render in a fixed order,
// whatever serialiser writes it. A bank's stored config travels as ordinary
// maps (its column is jsonb, which carries no order), so the one config value a
// reference model declares re-states its order here at render time.
type OrderedObject struct {
	// Keys lists member names in the order they are written; a member it does
	// not name follows, sorted.
	Keys []string
	// Members holds the values.
	Members map[string]any
}

// MarshalJSON writes the object with its members in order.
func (o OrderedObject) MarshalJSON() ([]byte, error) {
	if o.Members == nil {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range orderedKeys(o.Members, o.Keys) {
		if i > 0 {
			buf.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		encoded, err := json.Marshal(o.Members[key])
		if err != nil {
			return nil, err
		}
		buf.Write(encoded)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Reorder re-states the canonical member order on a decoded vocabulary value,
// so a value read back from storage renders the way the reference's serialiser
// writes it. Any other value is returned unchanged.
func Reorder(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		// Only the envelope a vocabulary value has is re-stated; a plain object
		// is left exactly as decoded.
		if attributes, ok := typed["attributes"]; ok && len(typed) == 1 {
			return OrderedObject{
				Keys:    []string{"attributes"},
				Members: map[string]any{"attributes": reorderGroups(attributes)},
			}
		}
	}
	return value
}

// reorderGroups re-states the order on a decoded attributes list.
func reorderGroups(attributes any) any {
	entries, ok := attributes.([]any)
	if !ok {
		return attributes
	}
	out := make([]any, len(entries))
	for i, entry := range entries {
		out[i] = orderedShape(asMap(entry), "group")
	}
	return out
}

// asMap reads a decoded object.
func asMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// orderedShape wraps a decoded object in the member order its shape declares,
// recursing into the nested shapes.
func orderedShape(object map[string]any, shape string) any {
	if object == nil {
		return object
	}
	out := make(map[string]any, len(object))
	for key, value := range object {
		switch key {
		case "attributes":
			out[key] = reorderGroups(value)
		case "values":
			out[key] = reorderValues(value)
		case "fields":
			out[key] = reorderFields(value)
		default:
			out[key] = value
		}
	}
	var keys []string
	switch shape {
	case "group":
		keys = groupMemberOrder
	case "field":
		keys = fieldMemberOrder
	case "value":
		keys = valueMemberOrder
	}
	return OrderedObject{Keys: keys, Members: out}
}

// reorderValues re-states the order on a decoded values list.
func reorderValues(values any) any {
	entries, ok := values.([]any)
	if !ok {
		return values
	}
	out := make([]any, len(entries))
	for i, entry := range entries {
		out[i] = orderedShape(asMap(entry), "value")
	}
	return out
}

// reorderFields re-states the order on a decoded fields object.
func reorderFields(fields any) any {
	object, ok := fields.(map[string]any)
	if !ok {
		return fields
	}
	out := make(map[string]any, len(object))
	for name, field := range object {
		out[name] = orderedShape(asMap(field), "field")
	}
	return out
}

// Problem is one entry of the reference's validation report for an
// entity_labels value, with the pieces a route renders as a framework
// validation item: the model's member path, the error kind, the message the
// library words, the value it refused, and the `ctx` a Literal mismatch
// carries.
type Problem struct {
	// Path is the member path inside the rejected model, dot-separated
	// ("values.0.value"); empty when the model itself was rejected.
	Path string
	// Kind is the reference's error type ("missing", "literal_error", ...).
	Kind string
	// Msg is the message the reference prints for the error.
	Msg string
	// Input is the value the caller supplied.
	Input any
	// Ctx is the error's context object, when the reference carries one.
	Ctx map[string]any
}

// Problems returns the reference's validation report for a raw entity_labels
// value as structured problems, or nil when the model accepts it. It is the
// item-level counterpart of ShapeProblems: a route whose request model declares
// entity_labels validates it in the same single pass as its other members, and
// reports each problem at the model's own path.
func Problems(raw any) []Problem {
	parsed, ok := Normalize(raw)
	if !ok || parsed == nil {
		return nil
	}
	groups, _ := parsed.([]any)
	var problems []problem
	for _, entry := range groups {
		object, isObject := entry.(map[string]any)
		if !isObject {
			problems = append(problems, problem{
				loc:   "",
				kind:  "model_type",
				msg:   "Input should be a valid dictionary or instance of LabelGroup",
				input: entry,
			})
			break
		}
		_, groupProblems := checkGroup(migrate(object))
		if len(groupProblems) > 0 {
			problems = append(problems, groupProblems...)
			break
		}
	}
	if len(problems) == 0 {
		return nil
	}
	out := make([]Problem, 0, len(problems))
	for _, p := range problems {
		out = append(out, Problem{
			Path: p.loc,
			Kind: p.kind,
			Msg:  p.msg,
			// A `missing` item echoes the object the member was missing from,
			// which the reference's model has already migrated and serialised:
			// its declared members in order, with the legacy migration's
			// `type` filled in.
			Input: problemInput(p),
			Ctx:   problemContext(p),
		})
	}
	return out
}

// problemInput is the value a problem's item carries. A `missing` member reports
// the model instance the member was missing from rather than the raw object, so
// it is the migrated group with its declared members present, in declaration
// order.
func problemInput(p problem) any {
	if p.kind != "missing" {
		return p.input
	}
	object, ok := p.input.(map[string]any)
	if !ok {
		return p.input
	}
	group, problems := checkGroup(migrate(object))
	if group == nil || len(problems) > 0 {
		return p.input
	}
	return group.render()
}

// problemContext is the `ctx` object the reference attaches to a problem, which
// only a Literal mismatch carries.
func problemContext(p problem) map[string]any {
	if p.kind != "literal_error" {
		return nil
	}
	expected := ""
	if _, msg, found := strings.Cut(p.msg, "Input should be "); found {
		expected = msg
	}
	if expected == "" {
		return nil
	}
	return map[string]any{"expected": expected}
}
