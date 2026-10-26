package memoryops

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
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
	// visionEnabled reports whether the deployment's write model can read
	// images, which decides whether an image block survives the canonicalizer.
	// The reference's canonicalize_item_content defers the vision gate to the
	// extraction step and, when the model cannot read images, drops every
	// attachment block and keeps only the text. It is not a wire field.
	visionEnabled bool
	// textIsCanonical marks content this build assembled itself as the final
	// canonical body rather than as blocks to canonicalize: the file route
	// converts an upload to markdown and hands that text straight to the retain,
	// so no image-block rule applies to it. It is not a wire field.
	textIsCanonical bool

	// metadataIssues records the metadata values the caller sent that are not
	// strings, in the order they appeared, so the field-level validator reports
	// each at its own key. The reference's metadata is dict[str, str], so a
	// non-string value is rejected rather than coerced or dropped.
	metadataIssues []metadataIssue
	// tagIssues records the tags-list elements that are not strings, so the
	// field-level validator reports each at its own position rather than the
	// element being dropped or the whole body failing to decode.
	tagIssues []tagIssue
	// raw is the item object the caller sent, so a rejection about a missing
	// member can name the item verbatim — the framework reports the object the
	// member was missing from as its input.
	raw any
	// updateModeInvalid records an update_mode outside the accepted literal set,
	// so the field-level validator reports it instead of the write proceeding
	// with an unrecognised value.
	updateModeInvalid bool
	updateModeInput   any
	// AttachmentIDs are the short attachment ids the upload was recorded
	// under. They are set by the file route after the bytes are stored, and
	// are internal rather than wire fields.
	AttachmentIDs []string `json:"-"`
	// Attachments are the decoded inline attachments this item carries, set by
	// storeInlineAttachments so the extractor can send them to the model. They
	// are internal rather than wire fields.
	Attachments []RetainAttachment `json:"-"`
	// AttachmentFilenames are the names the caller gave this item's attachments,
	// keyed by short id, set by storeInlineAttachments from the file blocks. A
	// name describes the reference rather than the bytes, so it is recorded on
	// the document's replayable parameters and restated by a reprocess.
	AttachmentFilenames map[string]string `json:"-"`
	// attachmentsDeclined records that the deployment's model cannot read
	// images, so this item's attachment blocks were dropped by the canonicalizer
	// and only their position as a paragraph break survives in the text. It is
	// the reference's "declined" canonicalization, not a wire field.
	attachmentsDeclined bool
	// Parts are the distinct content blocks this item folded together, in the
	// order the caller sent them; empty or one entry means Content is a single
	// block. Each block is chunked on its own boundaries, as the reference
	// chunks every content dict separately, so folding two items for one
	// extraction call must not reshape either one's chunks. Not a wire field.
	Parts []string
}

// rawObject is the item object as the caller sent it, which a rejection about
// one of its members carries as the item's input.
func (r RetainItem) rawObject() any {
	if r.raw != nil {
		return r.raw
	}
	return map[string]any{}
}

// metadataIssue is one metadata member that fails the reference's dict[str, str]
// shape: either the member itself is not an object (key is empty) or one of its
// values is not a string (key names the offending entry). input is the value the
// caller sent, reported verbatim.
type metadataIssue struct {
	key   string
	kind  string
	input any
}

// itemAlias mirrors RetainItem for decoding, so the custom UnmarshalJSON can
// decode all the ordinary members through it and then run the extra per-member
// checks the reference performs, without recursing back into itself.
type itemAlias RetainItem

// UnmarshalJSON decodes one retain item and records the member-level type
// failures the reference reports at the item's own paths: a metadata value that
// is not a string, and an update_mode outside its literal set. They are recorded
// rather than returned so the whole body is still described field by field, the
// way the reference's own validators report.
func (r *RetainItem) UnmarshalJSON(data []byte) error {
	var alias itemAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		// A `metadata` member that is not the object the contract pins is one of
		// the item's own field rules, not a structural failure: the reference
		// validates the item in one pass, so that failure is reported beside the
		// sibling fields' rules rather than hiding them. The member is therefore
		// left out of the decode and its failure is recorded below, which is what
		// lets a body carrying it still name every other field rule. Any other
		// failure keeps the shared decoder's abort, so the route renders the body
		// from the request contract exactly as before.
		rest, ok := withoutMember(data, "metadata")
		if !ok || json.Unmarshal(rest, &alias) != nil {
			return err
		}
	}
	*r = RetainItem(alias)
	r.raw = decodeAny(trimSpaceBytes(data))
	var probe struct {
		Metadata   json.RawMessage `json:"metadata"`
		UpdateMode json.RawMessage `json:"update_mode"`
		Tags       json.RawMessage `json:"tags"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	r.metadataIssues = metadataTypeIssues(probe.Metadata)
	r.updateModeInvalid, r.updateModeInput = updateModeTypeIssue(probe.UpdateMode)
	// The tags list is decoded again here only to collect the elements the
	// `list[str]` member validation rejects; the decoded value above is what
	// the write carries.
	if _, issues, err := decodeTagList(probe.Tags); err == nil {
		r.tagIssues = issues
	}
	return nil
}

// withoutMember re-encodes a JSON object without one member, so the rest of it
// can be decoded on its own. It reports false when data is not an object or does
// not carry the member, which is when there is nothing to leave out.
func withoutMember(data []byte, member string) ([]byte, bool) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, false
	}
	if _, present := members[member]; !present {
		return nil, false
	}
	delete(members, member)
	rest, err := json.Marshal(members)
	if err != nil {
		return nil, false
	}
	return rest, true
}

// metadataTypeIssues reports the metadata members that fail the dict[str, str]
// shape, in the order the caller sent them. A value that is not an object is
// reported at the member itself; otherwise each non-string value is reported at
// its key.
func metadataTypeIssues(raw json.RawMessage) []metadataIssue {
	trimmed := trimSpaceBytes(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] != '{' {
		return []metadataIssue{{kind: "dict_type", input: decodeAny(trimmed)}}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	issues := make([]metadataIssue, 0)
	if _, err := dec.Token(); err != nil { // consume the opening brace
		return nil
	}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			break
		}
		key, _ := keyToken.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			break
		}
		if !isJSONStringValue(value) {
			issues = append(issues, metadataIssue{key: key, kind: "string_type", input: decodeAny(value)})
		}
	}
	return issues
}

// isJSONStringValue reports whether a raw JSON value is a string literal.
func isJSONStringValue(raw json.RawMessage) bool {
	trimmed := trimSpaceBytes(raw)
	return len(trimmed) > 0 && trimmed[0] == '"'
}

// updateModeLiteral is the reference's accepted update_mode set.
var updateModeLiteral = []string{"replace", "append"}

// updateModeTypeIssue reports an update_mode outside the accepted literals. An
// absent or null value is valid (it means "unset"), so only a present value that
// is not one of the literals is reported.
func updateModeTypeIssue(raw json.RawMessage) (bool, any) {
	trimmed := trimSpaceBytes(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return false, nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err == nil && (value == "replace" || value == "append") {
		return false, nil
	}
	return true, decodeAny(trimmed)
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
	// blockIssues are the blocks the reference's content union rejected, filed
	// at the paths its framework names them at. The list is decoded past them
	// so the item's validation can report every failure in one pass, as the
	// reference's own single validation pass does.
	blockIssues []ContentBlockIssue
	// raw is the value the caller sent, kept so a rejection names it verbatim.
	raw any
	// rawJSON is the same value as the bytes the caller wrote, so a report can
	// echo it with the member order the reference's own decoder preserves.
	rawJSON json.RawMessage
	// present records that the content member carried a value. A missing
	// member is the model's "Field required" rejection, which the reference
	// keeps distinct from a value that is present and empty.
	present bool
}

// Present reports whether the item carried a content value at all.
func (c Content) Present() bool { return c.present }

// rawValue is the content value as the caller sent it, which is what the
// reference's validation items carry as their `input`.
func (c Content) rawValue() any {
	if c.raw != nil {
		return c.raw
	}
	if c.IsList {
		return []any{}
	}
	return c.Text
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
	// Filename is the name the caller gave a file block's attachment. The
	// contract declares it on the file block only — an image block carries none
	// — and it names the attachment for the model and for a later read, so it is
	// kept for the retain to record.
	Filename string `json:"filename,omitempty"`
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
	c.raw = decodeAny(trimmed)
	// The reference's report echoes the JSON it was given, so the raw document
	// keeps each object's member order rather than a Go map's sorted one.
	c.rawJSON = append(json.RawMessage(nil), trimmed...)
	if trimmed[0] == '"' {
		c.present = true
		return json.Unmarshal(data, &c.Text)
	}
	if trimmed[0] == '[' {
		c.present = true
		c.IsList = true
		// The block list is decoded by hand rather than into the block structs
		// directly: the reference validates every block against the tagged
		// union in one pass, so a block its model rejects is reported rather
		// than aborting the decode or being silently accepted.
		var blocks []json.RawMessage
		if err := json.Unmarshal(trimmed, &blocks); err != nil {
			return err
		}
		return c.decodeBlocks(blocks, c.raw)
	}
	return errors.New("content must be a string or a list of blocks")
}

// contentBlockModalPath is the loc segment the reference's framework files a
// block's own field failures under: the union's list arm plus the variant tag.
func contentBlockModalPath(index int, tag string) []string {
	return []string{contentUnionPath, strconv.Itoa(index), tag}
}

// contentUnionPath is the loc segment naming the content union's list arm. The
// framework builds it from the union's members, so it cannot drift from the
// tags the union accepts.
var contentUnionPath = "list[tagged-union[" + contentBlockTags[0] + "," +
	contentBlockTags[1] + "," + contentBlockTags[2] + "]]"

// contentBlockTags are the block discriminators the union accepts, in the order
// the framework lists them in a refusal.
var contentBlockTags = [3]string{"TextContentBlock", "ImageContentBlock", "FileContentBlock"}
