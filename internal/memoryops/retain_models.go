package memoryops

import (
	"encoding/json"
	"errors"
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
	Content           Content        `json:"content"`
	Timestamp         TimestampField `json:"timestamp"`
	Context           string         `json:"context,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
	DocumentID        string         `json:"document_id,omitempty"`
	Entities          []string       `json:"entities,omitempty"`
	ResolveEntities   bool           `json:"resolve_entities,omitempty"`
	Tags              TagList        `json:"tags,omitempty"`
	ObservationScopes []string       `json:"observation_scopes,omitempty"`
	Strategy          string         `json:"strategy,omitempty"`
	UpdateMode        string         `json:"update_mode,omitempty"`
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
// string sets the observation time.
type TimestampField struct {
	Set   bool
	Null  bool
	Value *time.Time
}

// UnmarshalJSON records the tri-state: absent is Set=false, null is Null=true,
// and an ISO string is parsed into Value.
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
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return errors.New("timestamp must be an ISO 8601 string")
	}
	t.Value = &parsed
	return nil
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
