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
	Content    Content         `json:"content"`
	Timestamp  TimestampField  `json:"timestamp"`
	Context    string          `json:"context,omitempty"`
	Metadata   map[string]any  `json:"metadata,omitempty"`
	DocumentID string          `json:"document_id,omitempty"`
	Entities   EntityInputList `json:"entities,omitempty"`
	// ResolveEntities is tri-state: absent defaults to true, matching the
	// reference, so an omitted flag resolves the supplied entity names against
	// the bank rather than taking them literally. Only an explicit false turns
	// resolution off. Ignored when Entities is empty.
	ResolveEntities   *bool     `json:"resolve_entities,omitempty"`
	Tags              TagList   `json:"tags,omitempty"`
	ObservationScopes ScopeList `json:"observation_scopes,omitempty"`
	Strategy          string    `json:"strategy,omitempty"`
	UpdateMode        string    `json:"update_mode,omitempty"`
	ForceReextract    bool      `json:"force_reextract,omitempty"`
	// AttachmentIDs are the short attachment ids the upload was recorded
	// under. They are set by the file route after the bytes are stored, and
	// are internal rather than wire fields.
	AttachmentIDs []string `json:"-"`
	// Attachments are the decoded inline attachments this item carries, set by
	// storeInlineAttachments so the extractor can send them to the model. They
	// are internal rather than wire fields.
	Attachments []RetainAttachment `json:"-"`
}

// RetainAttachment is one decoded inline attachment, carried from the retain
// ingress to the extractor.
type RetainAttachment struct {
	ShortID   string
	MediaType string
	Kind      string
	Data      []byte
}

// Content is a retained content value: either a plain text string or an
// ordered list of text and image blocks. It keeps the raw JSON so the string
// form and the block form both round-trip through the public wire shape.
type Content struct {
	Text   string
	Blocks []ContentBlock
	IsList bool
}

// ContentBlock is one ordered content block. Type is "text", "image" or
// "file"; a payload is one of the mutually exclusive Text and Source forms.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Source is an inline attachment payload. A block that carries one is
	// content-addressed on its decoded bytes, so it renders as the canonical
	// attachment placeholder instead of the raw bytes.
	Source *AttachmentSource `json:"source,omitempty"`
}

// AttachmentSource is an inline base64 attachment payload, matching the
// reference's content-block envelope.
type AttachmentSource struct {
	Type      string `json:"type,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

// attachmentShortID resolves the block's attachment short id from the digest of
// its inline payload. Empty means the block references no resolvable attachment.
func (b ContentBlock) attachmentShortID() string {
	if b.Source == nil || b.Source.Data == "" {
		return ""
	}
	data, ok := decodeAttachmentData(b.Source.Data)
	if !ok {
		return ""
	}
	return AttachmentShortID(attachmentHashOf(data))
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
// unchanged, while blocks are flattened the way the reference canonicalizes
// them — a text run keeps its position, and an attachment block becomes the
// canonical placeholder on its own paragraph, so the chunker's preferred
// boundary falls either side of it and a rendered chunk keeps the caption and
// image together.
func (c Content) Flatten() string {
	if !c.IsList {
		return c.Text
	}
	body := ""
	afterAttachment := false
	for _, block := range c.Blocks {
		if block.Type == "image" || block.Type == "file" {
			if id := block.attachmentShortID(); id != "" {
				body = padToBlankLine(body) + AttachmentPlaceholder(id)
			} else {
				// A block with no resolvable payload has no placeholder to
				// resolve; it degrades to a short note rather than raw bytes.
				body = padToBlankLine(body) + "[image]"
			}
			afterAttachment = true
			continue
		}
		if afterAttachment {
			body = padToBlankLine(body)
		}
		body += block.Text
		afterAttachment = false
	}
	return body
}

// padToBlankLine closes body with a paragraph break without doubling an
// existing one, so a placeholder sits on its own paragraph.
func padToBlankLine(body string) string {
	if body == "" {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n\n"
}

// InlineAttachment is one decoded inline attachment from a content item, in
// block order, carrying the bytes and the short id its placeholder names.
type InlineAttachment struct {
	BlockIndex int
	ShortID    string
	MediaType  string
	Kind       string
	Data       []byte
}

// InlineAttachments decodes the item's attachment blocks in order. A block
// whose payload cannot be decoded or carries no bytes is skipped rather than
// failing the whole item; the canonical text renders it as a note.
func (c Content) InlineAttachments() []InlineAttachment {
	if !c.IsList {
		return nil
	}
	out := make([]InlineAttachment, 0)
	for index, block := range c.Blocks {
		if block.Type != "image" && block.Type != "file" {
			continue
		}
		if block.Source == nil || block.Source.Data == "" {
			continue
		}
		data, ok := decodeAttachmentData(block.Source.Data)
		if !ok {
			continue
		}
		shortID := AttachmentShortID(attachmentHashOf(data))
		mediaType := block.Source.MediaType
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		kind := block.Type
		if kind == "" {
			kind = "image"
		}
		out = append(out, InlineAttachment{
			BlockIndex: index,
			ShortID:    shortID,
			MediaType:  mediaType,
			Kind:       kind,
			Data:       data,
		})
	}
	return out
}

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
// other shape is recorded as invalid rather than aborting the decode.
func (s *ScopeList) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		if err := json.Unmarshal(data, &s.Mode); err != nil {
			s.Invalid = true
			s.InvalidInput = decodeAny(trimmed)
			return nil
		}
	} else if err := json.Unmarshal(data, &s.Groups); err != nil {
		s.Invalid = true
		s.InvalidInput = decodeAny(trimmed)
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
