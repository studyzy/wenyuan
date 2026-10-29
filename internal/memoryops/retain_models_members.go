package memoryops

// This file decodes the retain item's remaining members: the event-date value,
// the caller-supplied entity list, the tag list and the observation-scope union.
// Each accepts the shapes the reference's model accepts and records the ones it
// rejects, so a body the reference refuses is refused here with the same items
// rather than being dropped or aborting the whole decode.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/validation"
)

// TimestampField is the item event-date value. It is tri-state: an unset value
// (omitted, JSON null, or the empty string) defaults to the write time, the
// sentinel "unset" stores no timestamp, and an ISO string sets the observation
// time. A JSON null is an *absent* timestamp rather than the "unset" sentinel:
// the reference's own client sends null for an ordinary write and expects the
// ingest clock to be used, and reserves the string "unset" for content that is
// deliberately timeless.
type TimestampField struct {
	Set   bool
	Null  bool
	Value *time.Time
	// Invalid records a value in none of the accepted forms. The decode records
	// it rather than failing the whole body, so the field-level validator can
	// report it at the field's own path, the way the reference's validators do.
	Invalid        bool
	InvalidMessage string
	InvalidInput   any
}

// TimestampUnset is the sentinel that asks for no timestamp. It is matched
// case-insensitively, as the contract states.
const TimestampUnset = "unset"

// UnmarshalJSON records the tri-state: absent is Set=false, null (or the
// "unset" sentinel, or an empty string) is Null=true, and an ISO string is
// parsed into Value. A value in none of those forms is recorded as invalid
// instead of aborting the decode, so it is reported like any other field error.
func (t *TimestampField) UnmarshalJSON(data []byte) error {
	t.Set = true
	trimmed := trimSpaceBytes(data)
	if string(trimmed) == "null" {
		// A JSON null is an absent timestamp, not the "unset" sentinel: it
		// defaults to the write time, exactly as an omitted key does.
		t.Set = false
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		t.Invalid = true
		t.InvalidMessage = "timestamp must be a string or datetime, got " + jsonTypeName(trimmed)
		t.InvalidInput = decodeAny(trimmed)
		return nil
	}
	if text == "" {
		// An empty string is likewise an absent timestamp.
		t.Set = false
		return nil
	}
	if strings.EqualFold(text, TimestampUnset) {
		t.Null = true
		return nil
	}
	parsed, err := parseISODate(text)
	if err != nil {
		t.Invalid = true
		t.InvalidMessage = "Invalid timestamp/event_date format: '" + text + "'. Expected ISO format like " +
			"'2024-01-15T10:30:00' or '2024-01-15T10:30:00Z', or the special value 'unset' to store without a timestamp."
		t.InvalidInput = text
		return nil
	}
	// The validator checks the year after the format parse, so a format error
	// keeps its own message and an "unset"/empty value never reaches either
	// (api/http.py MemoryItem.validate_timestamp).
	if validation.IsPlaceholderDate(parsed) {
		t.Invalid = true
		t.InvalidMessage = fmt.Sprintf("timestamp year %d is a placeholder, not a date; "+
			"send a real date or 'unset' to store without one.", parsed.Year())
		t.InvalidInput = text
		return nil
	}
	t.Value = &parsed
	return nil
}

// jsonTypeName names a decoded JSON value's type the way the reference's
// validator names it in its message.
func jsonTypeName(raw []byte) string {
	if len(raw) == 0 {
		return "NoneType"
	}
	switch raw[0] {
	case '"':
		return "str"
	case '[':
		return "list"
	case '{':
		return "dict"
	case 't', 'f':
		return "bool"
	case 'n':
		return "NoneType"
	}
	if strings.ContainsAny(string(raw), ".eE") {
		return "float"
	}
	return "int"
}

// decodeAny decodes a raw JSON value for the item's input field, falling back
// to the raw text when it cannot be decoded.
func decodeAny(raw []byte) any {
	var value any
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return string(raw)
}

// EntityInput is one entity a caller associates with retained content. The
// type is optional and is the caller's label for the entity rather than
// something derived from the text.
type EntityInput struct {
	Text string `json:"text"`
	Type string `json:"type,omitempty"`
	// Invalid records an element the reference's EntityInput model rejects: the
	// entities list takes objects only, so a bare string (or a JSON null) is
	// reported at its own path rather than silently accepted or dropped.
	// InvalidInput is the value the caller sent.
	Invalid      bool `json:"-"`
	InvalidInput any  `json:"-"`
	// memberIssues records the failures the element's own fields raise, in the
	// model's field order — text is the one required member — each with the
	// value the caller sent. They are recorded rather than returned so a body
	// whose other fields also fail is still described field by field.
	memberIssues []entityMemberIssue
}

// entityMemberIssue is one rejected member of an entity element.
type entityMemberIssue struct {
	kind  string
	field string
	input any
}

// UnmarshalJSON accepts only the canonical object form. The bare-string form is
// recorded as invalid rather than decoded: the reference's entities list takes
// EntityInput objects, and a string element fails validation there. An object
// element is decoded member by member so its own field failures are recorded
// beside the element's — an element with no text is a missing member, and one
// whose text is not a string is a type failure.
func (e *EntityInput) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		e.Invalid = true
		e.InvalidInput = decodeAny(trimmed)
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &members); err != nil {
		return errors.New("entity must be an object with a text field")
	}
	text, present := members["text"]
	if !present {
		e.memberIssues = append(e.memberIssues,
			entityMemberIssue{kind: "missing", field: "text", input: decodeAny(trimmed)})
	} else if raw := trimSpaceBytes(text); len(raw) == 0 || raw[0] != '"' {
		e.memberIssues = append(e.memberIssues,
			entityMemberIssue{kind: "string_type", field: "text", input: decodeAny(raw)})
	} else if err := json.Unmarshal(raw, &e.Text); err != nil {
		return errors.New("entity must be an object with a text field")
	}
	if kind, hasType := members["type"]; hasType {
		raw := trimSpaceBytes(kind)
		// A null type is the caller leaving the label unset, which the
		// reference accepts as absent.
		if len(raw) > 0 && string(raw) != "null" {
			if raw[0] != '"' {
				e.memberIssues = append(e.memberIssues,
					entityMemberIssue{kind: "string_type", field: "type", input: decodeAny(raw)})
				return nil
			}
			if err := json.Unmarshal(raw, &e.Type); err != nil {
				return errors.New("entity must be an object with a text field")
			}
		}
	}
	return nil
}

// EntityInputList is the list of entities a caller supplied. Decoding is
// per-element so a list mixing the object and string forms is accepted.
type EntityInputList []EntityInput

// Texts returns the entity names in supplied order, dropping blanks so an
// empty entry cannot become an entity with no name.
func (l EntityInputList) Texts() []string {
	out := make([]string, 0, len(l))
	for _, entity := range l {
		if name := trimSpaceString(entity.Text); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// TagList accepts tags as a JSON list or as a JSON-encoded string that decodes
// to a list, coercing the latter back into a list so the write still succeeds.
// A bare non-JSON string is treated as a single tag.
type TagList []string

// UnmarshalJSON coerces the accepted tag forms into a list. The per-element
// failures the reference reports are collected by decodeTagList, which the
// item decoder calls; a direct decode of the type accepts the forms as before.
func (t *TagList) UnmarshalJSON(data []byte) error {
	list, _, err := decodeTagList(data)
	if err != nil {
		return err
	}
	*t = list
	return nil
}

// tagIssue is one element of a tags list the reference's `list[str]` member
// validation rejects, named by its position in that list and carrying the value
// the caller sent.
type tagIssue struct {
	index int
	input any
}

// decodeTagList decodes the accepted tag forms into a list, reporting the
// positions whose element is not a string instead of dropping or rejecting the
// whole list: the reference's member validation names each bad element at its
// own path, and the item's other fields are still reported beside it.
func decodeTagList(data []byte) (TagList, []tagIssue, error) {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil, nil
	}
	if trimmed[0] != '[' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, nil, errors.New("tags must be a list or a string")
		}
		// The string form is a JSON-encoded list when it parses as one; its
		// elements are then held to the same `list[str]` rule as a real list,
		// rather than the encoded text being taken on trust.
		if encoded := trimSpaceBytes([]byte(text)); len(encoded) > 0 && encoded[0] == '[' {
			return decodeTagList(encoded)
		}
		return TagList{text}, nil, nil
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(trimmed, &elements); err != nil {
		return nil, nil, err
	}
	list := make(TagList, 0, len(elements))
	var issues []tagIssue
	for index, element := range elements {
		raw := trimSpaceBytes(element)
		if len(raw) == 0 || raw[0] != '"' {
			issues = append(issues, tagIssue{index: index, input: decodeAny(raw)})
			continue
		}
		var tag string
		if err := json.Unmarshal(raw, &tag); err != nil {
			return nil, nil, err
		}
		list = append(list, tag)
	}
	return list, issues, nil
}

// ObservationScopeModes are the named scope literals the write accepts.
var ObservationScopeModes = []string{"per_tag", "combined", "all_combinations", "shared"}

// ScopeList is the observation scope a caller supplied. The contract accepts
// two forms: one of the named mode literals, or a list of explicit tag groups
// where each group is itself a list of tag names. Both are kept because they
// mean different things — a mode asks the server to derive the groups, an
// explicit list states them.
type ScopeList struct {
	// Mode is the named literal, empty when the caller sent explicit groups.
	Mode string
	// Groups is the explicit tag groups, nil when the caller sent a mode.
	Groups [][]string
	// Invalid records a value in neither accepted form. The decode records it
	// rather than failing the whole body, so the field-level validator reports
	// it at the field's own path.
	Invalid      bool
	InvalidInput any
	// raw keeps the accepted JSON so the value can be forwarded to storage
	// without re-encoding, which would have to guess which form was sent.
	raw json.RawMessage
}

// IsZero reports whether no scope was supplied.
func (s ScopeList) IsZero() bool { return len(s.raw) == 0 }

// RawJSON returns the accepted value in the form it arrived, or nil when no
// scope was supplied.
func (s ScopeList) RawJSON() json.RawMessage {
	if len(s.raw) == 0 {
		return nil
	}
	return s.raw
}

// UnmarshalJSON accepts the named literal and the list-of-groups forms. Any
// other shape — a string outside the literal set included — is recorded as
// invalid rather than aborting the decode, so the field-level validator reports
// the union's own failures at the field's path.
func (s *ScopeList) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	value := decodeAny(trimmed)
	switch typed := value.(type) {
	case string:
		if !containsToken(ObservationScopeModes, typed) {
			s.Invalid = true
			s.InvalidInput = value
			return nil
		}
		s.Mode = typed
	case []any:
		groups := make([][]string, 0, len(typed))
		for _, element := range typed {
			tags, ok := element.([]any)
			if !ok {
				s.Invalid = true
				s.InvalidInput = value
				return nil
			}
			group := make([]string, 0, len(tags))
			for _, tag := range tags {
				text, ok := tag.(string)
				if !ok {
					s.Invalid = true
					s.InvalidInput = value
					return nil
				}
				group = append(group, text)
			}
			groups = append(groups, group)
		}
		s.Groups = groups
	default:
		s.Invalid = true
		s.InvalidInput = value
		return nil
	}
	s.raw = append(json.RawMessage(nil), trimmed...)
	return nil
}

// trimSpaceString trims the surrounding whitespace from a string.
func trimSpaceString(value string) string {
	return strings.TrimSpace(value)
}

// trimSpaceBytes trims the surrounding whitespace from raw JSON.
func trimSpaceBytes(data []byte) []byte {
	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == '\t' || data[start] == '\n' || data[start] == '\r') {
		start++
	}
	end := len(data)
	for end > start && (data[end-1] == ' ' || data[end-1] == '\t' || data[end-1] == '\n' || data[end-1] == '\r') {
		end--
	}
	return data[start:end]
}
