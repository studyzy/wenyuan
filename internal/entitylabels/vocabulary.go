// Package entitylabels ports the reference's bank-level controlled vocabulary
// of key:value classification labels.
//
// A bank declares label groups (dimensions). Each group has a type:
//
//	value        one of a fixed set of allowed values
//	multi-values many of a fixed set
//	text         a free string
//	multi-text   free strings
//	map          a list of nested objects whose fields are themselves typed
//
// A model's selection becomes an entity named "key:value"; a group flagged
// tag:true also projects its selection into the unit's tags. The package is
// dependency-free — it imports no other package of this module and no model
// boundary — so both the extraction and the storage areas can share the same
// vocabulary type without an import cycle.
package entitylabels

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The label group and map field types.
const (
	TypeValue       = "value"
	TypeMultiValues = "multi-values"
	TypeText        = "text"
	TypeMultiText   = "multi-text"
	TypeMap         = "map"
)

// Value is one allowed value of an enum-constrained group or map field.
type Value struct {
	Value       string
	Description string
}

// Field is one field of a map group. A field of type map nests further fields,
// which is what lets a structured entity describe a tree.
type Field struct {
	Type        string
	Description string
	Values      []Value
	Fields      map[string]Field
}

// Group is one label dimension of the vocabulary.
type Group struct {
	Key         string
	Description string
	Type        string
	// Optional marks a value group the model may leave unset. Only value
	// groups read it; the other types are always optional.
	Optional bool
	// Tag projects a selection of this group into the unit's tags as well as
	// into its entities.
	Tag    bool
	Values []Value
	Fields map[string]Field
}

// Vocabulary is a bank's parsed controlled vocabulary.
type Vocabulary struct {
	Attributes []Group
}

// Parse reads a raw entity-labels config into a Vocabulary.
//
// It accepts the two config shapes the reference does: a bare list of group
// objects, or an object carrying that list under "attributes". A nil, an empty
// list or an object with no attributes yields a nil Vocabulary — "no
// vocabulary configured" — rather than an error.
//
// A definition the model could not honour is an error rather than a silent
// drop, so a caller can surface it as a 422: an unknown group type, a group
// that is not an object, or a values list whose entries are not
// {value, description} objects.
func Parse(raw any) (*Vocabulary, error) {
	normalised, err := normalise(raw)
	if err != nil {
		return nil, err
	}
	switch typed := normalised.(type) {
	case nil:
		return nil, nil
	case []any:
		return parseAttributes(typed)
	case map[string]any:
		attrs, present := typed["attributes"]
		if !present || attrs == nil {
			return nil, nil
		}
		list, ok := attrs.([]any)
		if !ok {
			return nil, fmt.Errorf("entity labels: attributes must be a list, got %T", attrs)
		}
		return parseAttributes(list)
	default:
		return nil, fmt.Errorf("entity labels: expected a list or an object with attributes, got %T", normalised)
	}
}

// normalise reduces the many shapes a caller may hold the config in to the two
// the parser understands (list and object). Anything but a decoded JSON list or
// object is round-tripped through JSON, so a typed config value is read exactly
// as its JSON form would be.
func normalise(raw any) (any, error) {
	switch raw.(type) {
	case nil:
		return nil, nil
	case []any, map[string]any:
		return raw, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("entity labels: could not be read: %w", err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("entity labels: could not be read: %w", err)
	}
	return decoded, nil
}

// parseAttributes reads the top-level group list, migrating each entry's legacy
// keys first.
func parseAttributes(raw []any) (*Vocabulary, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	attributes := make([]Group, 0, len(raw))
	for index, entry := range raw {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entity labels: group %d must be an object, got %T", index, entry)
		}
		group, err := parseGroup(migrate(object))
		if err != nil {
			return nil, fmt.Errorf("entity labels: group %d: %w", index, err)
		}
		attributes = append(attributes, group)
	}
	return &Vocabulary{Attributes: attributes}, nil
}

// migrate folds the reference's legacy free_values/multi_value keys into the
// type field, exactly as the reference does: free_values ⇒ text, multi_value ⇒
// multi-values, and neither ⇒ value. A group that already names a type is left
// alone so the new shape wins.
func migrate(raw map[string]any) map[string]any {
	if _, ok := raw["type"]; ok {
		return raw
	}
	patched := make(map[string]any, len(raw))
	for key, value := range raw {
		patched[key] = value
	}
	switch {
	case truthy(patched["free_values"]):
		patched["type"] = TypeText
	case truthy(patched["multi_value"]):
		patched["type"] = TypeMultiValues
	default:
		patched["type"] = TypeValue
	}
	delete(patched, "free_values")
	delete(patched, "multi_value")
	return patched
}

// parseGroup reads one label group.
func parseGroup(raw map[string]any) (Group, error) {
	key, present := raw["key"]
	if !present {
		return Group{}, fmt.Errorf("missing key")
	}
	keyText, ok := key.(string)
	if !ok {
		return Group{}, fmt.Errorf("key must be a string, got %T", key)
	}
	group := Group{
		Key:         keyText,
		Description: optionalString(raw["description"]),
		Optional:    true,
		Type:        optionalString(raw["type"]),
	}
	// A group reaches here with a type (the legacy migration sets one), but a
	// group whose type is an empty string is a value group by default.
	if group.Type == "" {
		group.Type = TypeValue
	}
	if !validType(group.Type) {
		return Group{}, fmt.Errorf("unsupported label group type %q", group.Type)
	}
	if value, ok := raw["optional"].(bool); ok {
		group.Optional = value
	}
	if value, ok := raw["tag"].(bool); ok {
		group.Tag = value
	}
	values, err := parseValues(raw["values"])
	if err != nil {
		return Group{}, err
	}
	group.Values = values
	fields, err := parseFields(raw["fields"])
	if err != nil {
		return Group{}, err
	}
	group.Fields = fields
	return group, nil
}

// parseField reads one map field. Its type defaults to text, which is the
// reference's MapField default; unlike a group it carries no legacy keys and no
// tag flag.
func parseField(name string, raw map[string]any) (Field, error) {
	field := Field{
		Type:        optionalString(raw["type"]),
		Description: optionalString(raw["description"]),
	}
	if field.Type == "" {
		field.Type = TypeText
	}
	if !validType(field.Type) {
		return Field{}, fmt.Errorf("field %q has unsupported type %q", name, field.Type)
	}
	values, err := parseValues(raw["values"])
	if err != nil {
		return Field{}, fmt.Errorf("field %q: %w", name, err)
	}
	field.Values = values
	fields, err := parseFields(raw["fields"])
	if err != nil {
		return Field{}, fmt.Errorf("field %q: %w", name, err)
	}
	field.Fields = fields
	return field, nil
}

// parseFields reads a map group's (or nested map field's) sub-fields.
func parseFields(raw any) (map[string]Field, error) {
	if raw == nil {
		return nil, nil
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("fields must be an object, got %T", raw)
	}
	if len(object) == 0 {
		return nil, nil
	}
	out := make(map[string]Field, len(object))
	for name, entry := range object {
		nested, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field %q must be an object, got %T", name, entry)
		}
		field, err := parseField(name, nested)
		if err != nil {
			return nil, err
		}
		out[name] = field
	}
	return out, nil
}

// parseValues reads an allowed-value list. A list whose entries are not
// {value: string, description: string} objects is malformed and errors, so a
// typo in the vocabulary is caught rather than silently dropped.
func parseValues(raw any) ([]Value, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("values must be a list, got %T", raw)
	}
	out := make([]Value, 0, len(list))
	for index, entry := range list {
		object, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("value %d must be an object, got %T", index, entry)
		}
		text, ok := object["value"].(string)
		if !ok {
			return nil, fmt.Errorf("value %d must carry a string value", index)
		}
		out = append(out, Value{Value: text, Description: optionalString(object["description"])})
	}
	return out, nil
}

// validType reports whether a type name is one of the five the reference
// defines.
func validType(name string) bool {
	switch name {
	case TypeValue, TypeMultiValues, TypeText, TypeMultiText, TypeMap:
		return true
	default:
		return false
	}
}

// truthy mirrors Python's truthiness for the legacy migration: a false, zero,
// empty string or missing value is false.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return false
	}
}

// optionalString reads a string field that may be absent, returning "".
func optionalString(value any) string {
	text, _ := value.(string)
	return text
}

// Empty reports whether no vocabulary is configured, so the zero state (a nil
// Vocabulary) is empty too.
func (v *Vocabulary) Empty() bool {
	return v == nil || len(v.Attributes) == 0
}

// CanonicalLabel renders one selected value of a group as the entity name the
// reference stores: "key:value".
func CanonicalLabel(key, value string) string {
	return key + ":" + value
}

// Label renders one of this group's selected values as its canonical entity
// name.
func (g Group) Label(value string) string {
	return CanonicalLabel(g.Key, value)
}

// AllowedValues returns the group's declared values in order, dropping the
// empty ones the schema would ignore.
func (g Group) AllowedValues() []string {
	out := make([]string, 0, len(g.Values))
	for _, value := range g.Values {
		if value.Value != "" {
			out = append(out, value.Value)
		}
	}
	return out
}

// AllowedValue reports the declared value matching value case-insensitively,
// which is how the reference validates a model's answer. The declared spelling
// is returned so a label is canonical however the model cased it.
func (g Group) AllowedValue(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	for _, allowed := range g.Values {
		if strings.EqualFold(allowed.Value, trimmed) && allowed.Value != "" {
			return allowed.Value, true
		}
	}
	return "", false
}

// TagKeys returns the lowercased keys of the groups flagged tag:true, which is
// the set that decides whether a label is also a unit tag.
func (v *Vocabulary) TagKeys() map[string]bool {
	if v == nil {
		return nil
	}
	out := make(map[string]bool)
	for _, group := range v.Attributes {
		if group.Tag && group.Key != "" {
			out[strings.ToLower(group.Key)] = true
		}
	}
	return out
}

// TagLabels returns the subset of canonical labels whose key is a tag group,
// preserving order and dropping repeats. It is the write-side projection the
// reference calls split_label_tags.
func (v *Vocabulary) TagLabels(labels []string) []string {
	keys := v.TagKeys()
	if len(keys) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, label := range labels {
		key, _, found := strings.Cut(label, ":")
		if !found || !keys[strings.ToLower(key)] || seen[label] {
			continue
		}
		seen[label] = true
		out = append(out, label)
	}
	return out
}

// Group returns the group with the given key (case-insensitively).
func (v *Vocabulary) Group(key string) (Group, bool) {
	if v == nil {
		return Group{}, false
	}
	for _, group := range v.Attributes {
		if strings.EqualFold(group.Key, key) {
			return group, true
		}
	}
	return Group{}, false
}

// TagCandidates returns every tag the tag:true groups could project onto a fact,
// for a write-scope check. A closed group yields one key:value per allowed
// value; an open group (text, multi-text, map) any value, represented as the
// wildcard "key:*", so only a caller allowed to write that whole namespace
// passes. It is the write-side counterpart of TagLabels.
func (v *Vocabulary) TagCandidates() []string {
	if v == nil {
		return nil
	}
	var out []string
	for _, group := range v.Attributes {
		if !group.Tag || group.Key == "" {
			continue
		}
		switch group.Type {
		case TypeText, TypeMultiText, TypeMap:
			out = append(out, group.Key+":*")
		default:
			for _, value := range group.Values {
				if value.Value != "" {
					out = append(out, group.Key+":"+value.Value)
				}
			}
		}
	}
	return out
}
