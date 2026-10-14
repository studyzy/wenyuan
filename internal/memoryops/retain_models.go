package memoryops

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RetainRequest is the memory write body accepted by the bank-scoped write
// route. Every field name is public surface.
type RetainRequest struct {
	Items        []RetainItem `json:"items"`
	Async        bool         `json:"async,omitempty"`
	OperationID  string       `json:"operation_id,omitempty"`
	DocumentTags []string     `json:"document_tags,omitempty"`
}

// RetainItem is one ordered content item inside a write. Every field name is
// public surface.
type RetainItem struct {
	Content           Content         `json:"content"`
	Timestamp         TimestampField  `json:"timestamp"`
	Context           string          `json:"context,omitempty"`
	Metadata          map[string]any  `json:"metadata,omitempty"`
	DocumentID        string          `json:"document_id,omitempty"`
	Entities          EntityInputList `json:"entities,omitempty"`
	ResolveEntities   bool            `json:"resolve_entities,omitempty"`
	Tags              TagList         `json:"tags,omitempty"`
	ObservationScopes ScopeList       `json:"observation_scopes,omitempty"`
	Strategy          string          `json:"strategy,omitempty"`
	UpdateMode        string          `json:"update_mode,omitempty"`
	// AttachmentIDs are the short attachment ids the upload was recorded
	// under. They are set by the file route after the bytes are stored, and
	// are internal rather than wire fields.
	AttachmentIDs []string `json:"-"`
}

// Content is a retained content value: either a plain text string or an
// ordered list of text and image blocks. It keeps the raw JSON so the string
// form and the block form both round-trip through the public wire shape.
type Content struct {
	Text   string
	Blocks []ContentBlock
	IsList bool
}

// ContentBlock is one ordered content block. Type is "text" or "image"; the
// text and image payloads are mutually exclusive per block.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// UnmarshalJSON accepts both the string and the ordered-block content forms.
func (c *Content) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		return json.Unmarshal(data, &c.Text)
	}
	if trimmed[0] == '[' {
		c.IsList = true
		return json.Unmarshal(data, &c.Blocks)
	}
	return errors.New("content must be a string or a list of blocks")
}

// MarshalJSON emits the canonical string form or the ordered-block form.
func (c Content) MarshalJSON() ([]byte, error) {
	if c.IsList {
		return json.Marshal(c.Blocks)
	}
	return json.Marshal(c.Text)
}

// HasImage reports whether any block carries an image payload.
func (c Content) HasImage() bool {
	for _, block := range c.Blocks {
		if block.Type == "image" {
			return true
		}
	}
	return false
}

// Flatten renders the content as canonical text: a plain string is returned
// unchanged, while blocks are joined into placeholder text so raw bytes never
// enter the pipeline.
func (c Content) Flatten() string {
	if !c.IsList {
		return c.Text
	}
	out := ""
	for i, block := range c.Blocks {
		if i > 0 {
			out += "\n"
		}
		switch block.Type {
		case "image":
			out += "[image]"
		default:
			out += block.Text
		}
	}
	return out
}

// TimestampField is the item event-date value. It is tri-state: an unset value
// (omitted) defaults to now, an explicit null stores no timestamp, and an ISO
// string sets the observation time. The sentinel "unset" and the empty string
// both mean the same as null: store the item without a timestamp.
type TimestampField struct {
	Set   bool
	Null  bool
	Value *time.Time
}

// TimestampUnset is the sentinel that asks for no timestamp. It is matched
// case-insensitively, as the contract states.
const TimestampUnset = "unset"

// UnmarshalJSON records the tri-state: absent is Set=false, null (or the
// "unset" sentinel, or an empty string) is Null=true, and an ISO string is
// parsed into Value.
func (t *TimestampField) UnmarshalJSON(data []byte) error {
	t.Set = true
	trimmed := trimSpaceBytes(data)
	if string(trimmed) == "null" {
		t.Null = true
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return errors.New("timestamp must be an ISO string, null, or omitted")
	}
	if text == "" || strings.EqualFold(text, TimestampUnset) {
		t.Null = true
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return errors.New("timestamp must be an ISO 8601 string")
	}
	t.Value = &parsed
	return nil
}

// EntityInput is one entity a caller associates with retained content. The
// type is optional and is the caller's label for the entity rather than
// something derived from the text.
type EntityInput struct {
	Text string `json:"text"`
	Type string `json:"type,omitempty"`
}

// UnmarshalJSON accepts the canonical object form and the bare-string form.
// The string form carries no type, so an entity sent that way is untyped.
func (e *EntityInput) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		return json.Unmarshal(data, &e.Text)
	}
	type entityAlias EntityInput
	var alias entityAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return errors.New("entity must be a string or an object with a text field")
	}
	*e = EntityInput(alias)
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

// UnmarshalJSON coerces the accepted tag forms into a list.
func (t *TagList) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '[' {
		var list []string
		if err := json.Unmarshal(data, &list); err != nil {
			return err
		}
		*t = list
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return errors.New("tags must be a list or a string")
	}
	var list []string
	if err := json.Unmarshal([]byte(text), &list); err == nil {
		*t = list
		return nil
	}
	*t = []string{text}
	return nil
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

// UnmarshalJSON accepts the named literal and the list-of-groups forms.
func (s *ScopeList) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		if err := json.Unmarshal(data, &s.Mode); err != nil {
			return err
		}
	} else if err := json.Unmarshal(data, &s.Groups); err != nil {
		return errors.New("observation_scopes must be a mode name or a list of tag groups")
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
