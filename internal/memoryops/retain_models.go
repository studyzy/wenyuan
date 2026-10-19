package memoryops

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
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

// contentMediaTypeRe mirrors the reference's MIME-shape check on an attachment
// source's media type.
var contentMediaTypeRe = regexp.MustCompile(`^[\w.+-]+/[\w.+-]+$`)

// decodeBlocks validates and records each block of a block-form content value.
// A block the union rejects is kept as an issue naming its path rather than a
// decoded block, so the item's validation reports every failure at once.
func (c *Content) decodeBlocks(blocks []json.RawMessage, whole any) error {
	c.Blocks = make([]ContentBlock, 0, len(blocks))
	for index, raw := range blocks {
		trimmed := trimSpaceBytes(raw)
		var members map[string]json.RawMessage
		if len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &members) != nil {
			c.blockIssues = append(c.blockIssues, ContentBlockIssue{
				Loc:   []string{contentUnionPath, strconv.Itoa(index)},
				Kind:  "model_attributes_type",
				Msg:   "Input should be a valid dictionary or object to extract fields from",
				Input: decodeAny(trimmed),
			})
			continue
		}
		tagRaw, hasTag := members["type"]
		var tag string
		if hasTag && len(trimSpaceBytes(tagRaw)) > 0 && trimSpaceBytes(tagRaw)[0] == '"' {
			_ = json.Unmarshal(tagRaw, &tag)
		}
		if !hasTag || tag == "" {
			c.blockIssues = append(c.blockIssues, ContentBlockIssue{
				Loc:   []string{contentUnionPath, strconv.Itoa(index)},
				Kind:  "union_tag_not_found",
				Msg:   "Unable to extract tag using discriminator 'type'",
				Input: decodeAny(trimmed),
				Ctx:   map[string]any{"discriminator": "'type'"},
			})
			continue
		}
		if tag != "text" && tag != "image" && tag != "file" {
			tags := "'" + strings.Join(contentBlockTagNames, "', '") + "'"
			c.blockIssues = append(c.blockIssues, ContentBlockIssue{
				Loc:   []string{contentUnionPath, strconv.Itoa(index)},
				Kind:  "union_tag_invalid",
				Msg:   "Input tag '" + tag + "' found using 'type' does not match any of the expected tags: " + tags,
				Input: decodeAny(trimmed),
				Ctx:   map[string]any{"discriminator": "'type'", "expected_tags": tags, "tag": tag},
			})
			continue
		}
		block, issues := decodeContentBlock(index, tag, members, decodeAny(trimmed))
		c.blockIssues = append(c.blockIssues, issues...)
		c.Blocks = append(c.Blocks, block)
	}
	return nil
}

// contentBlockTagNames are the accepted tags as a caller reads them.
var contentBlockTagNames = []string{"text", "image", "file"}

// decodeContentBlock decodes one tagged block and reports its own field
// failures at the paths the framework names them at.
func decodeContentBlock(index int, tag string, members map[string]json.RawMessage,
	whole any) (ContentBlock, []ContentBlockIssue) {
	block := ContentBlock{Type: tag}
	var issues []ContentBlockIssue
	path := contentBlockModalPath(index, tag)
	if tag == "text" {
		text, present := members["text"]
		switch {
		case !present:
			issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, path...), "text"),
				Kind: "missing", Msg: "Field required", Input: whole})
		case len(trimSpaceBytes(text)) == 0 || trimSpaceBytes(text)[0] != '"':
			issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, path...), "text"),
				Kind: "string_type", Msg: "Input should be a valid string", Input: decodeAny(trimSpaceBytes(text))})
		default:
			_ = json.Unmarshal(text, &block.Text)
		}
		return block, issues
	}
	rawSource, present := members["source"]
	sourcePath := append(append([]string{}, path...), "source")
	if !present {
		issues = append(issues, ContentBlockIssue{Loc: sourcePath, Kind: "missing", Msg: "Field required", Input: whole})
		return block, issues
	}
	trimmedSource := trimSpaceBytes(rawSource)
	var sourceMembers map[string]json.RawMessage
	if len(trimmedSource) == 0 || trimmedSource[0] != '{' || json.Unmarshal(trimmedSource, &sourceMembers) != nil {
		issues = append(issues, ContentBlockIssue{Loc: sourcePath, Kind: "model_attributes_type",
			Msg: "Input should be a valid dictionary or object to extract fields from", Input: decodeAny(trimmedSource)})
		return block, issues
	}
	source := AttachmentSource{}
	if rawType, ok := sourceMembers["type"]; ok {
		raw := trimSpaceBytes(rawType)
		if len(raw) == 0 || raw[0] != '"' || string(raw) != `"base64"` {
			issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, sourcePath...), "type"),
				Kind: "literal_error", Msg: "Input should be 'base64'",
				Input: decodeAny(raw), Ctx: map[string]any{"expected": "'base64'"}})
		} else {
			source.Type = "base64"
		}
	}
	if rawMedia, ok := sourceMembers["media_type"]; !ok {
		issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, sourcePath...), "media_type"),
			Kind: "missing", Msg: "Field required", Input: decodeAny(trimmedSource)})
	} else if raw := trimSpaceBytes(rawMedia); len(raw) == 0 || raw[0] != '"' {
		issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, sourcePath...), "media_type"),
			Kind: "string_type", Msg: "Input should be a valid string", Input: decodeAny(raw)})
	} else {
		_ = json.Unmarshal(rawMedia, &source.MediaType)
		if !contentMediaTypeRe.MatchString(source.MediaType) {
			issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, sourcePath...), "media_type"),
				Kind: "value_error", Msg: "Value error, media_type must look like 'type/subtype', got '" +
					source.MediaType + "'", Input: source.MediaType, Ctx: map[string]any{"error": map[string]any{}}})
		}
	}
	if rawData, ok := sourceMembers["data"]; !ok {
		issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, sourcePath...), "data"),
			Kind: "missing", Msg: "Field required", Input: decodeAny(trimmedSource)})
	} else if raw := trimSpaceBytes(rawData); len(raw) == 0 || raw[0] != '"' {
		issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, sourcePath...), "data"),
			Kind: "string_type", Msg: "Input should be a valid string", Input: decodeAny(raw)})
	} else {
		_ = json.Unmarshal(rawData, &source.Data)
	}
	if tag == "file" {
		if rawName, ok := members["filename"]; ok {
			raw := trimSpaceBytes(rawName)
			if len(raw) == 0 || string(raw) == "null" {
				block.Filename = ""
			} else if raw[0] != '"' {
				issues = append(issues, ContentBlockIssue{Loc: append(append([]string{}, path...), "filename"),
					Kind: "string_type", Msg: "Input should be a valid string", Input: decodeAny(raw)})
			} else {
				_ = json.Unmarshal(rawName, &block.Filename)
			}
		}
	}
	block.Source = &source
	return block, issues
}

// ContentBlockIssue is one block the reference's content union rejects, filed
// at the path the framework names it at.
type ContentBlockIssue struct {
	Loc   []string
	Kind  string
	Msg   string
	Input any
	Ctx   any
}

// CarriesContent reports whether the value holds anything to extract: a
// non-blank string, or a block list with an attachment block or a non-blank
// text block. Anything else is the reference's "content cannot be empty".
func (c Content) CarriesContent() bool {
	if !c.IsList {
		return strings.TrimSpace(c.Text) != ""
	}
	for _, block := range c.Blocks {
		if block.Type == "image" || block.Type == "file" {
			return true
		}
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

// attachmentPayloadFailure reports the first attachment block whose payload the
// reference cannot canonicalize: an empty payload or one that is not decodable
// base64. pathPrefix is the loc prefix the sentence opens with. The reference
// reports these as 400 request errors rather than validation items, because
// they are raised while canonicalizing, after the model validated.
func (c Content) attachmentPayloadFailure(pathPrefix string) string {
	if !c.IsList {
		return ""
	}
	for index, block := range c.Blocks {
		if block.Type != "image" && block.Type != "file" {
			continue
		}
		// A block with no source at all is the model's own "Field required"
		// rejection, reported as a validation item before canonicalization runs.
		if block.Source == nil {
			continue
		}
		where := pathPrefix + "[" + strconv.Itoa(index) + "]"
		if block.Source.Data == "" {
			return where + ": attachment source data is empty"
		}
		if _, err := base64.StdEncoding.DecodeString(block.Source.Data); err != nil {
			return where + ": attachment source data is not valid base64: Only base64 data is allowed"
		}
	}
	return ""
}

// scrubPlaceholders strips placeholder-shaped substrings from caller-authored
// text. Only the canonicalizer may mint a placeholder, so a caller that writes
// the token by hand cannot have extraction resolve it to an attachment its
// document never carried. allowedIds exempts the document's own attachments,
// which makes re-sending a document's body an ordinary edit rather than a write
// that silently deletes every screenshot in it.
func scrubPlaceholders(text string, allowedIDs map[string]struct{}) string {
	matches := placeholderLookalikeRe.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	cursor := 0
	for _, match := range matches {
		out.WriteString(text[cursor:match[0]])
		token := text[match[0]:match[1]]
		if full := placeholderRe.FindStringSubmatch(token); full != nil {
			if _, ok := allowedIDs[full[1]]; ok {
				out.WriteString(token)
			}
		}
		cursor = match[1]
	}
	out.WriteString(text[cursor:])
	return out.String()
}

var (
	// placeholderRe matches a well-formed placeholder and captures its short id.
	placeholderRe = regexp.MustCompile(regexp.QuoteMeta(placeholderOpen) + regexp.QuoteMeta(placeholderBody) +
		"([0-9a-f]{" + strconv.Itoa(attachmentShortIDLength) + "})" + regexp.QuoteMeta(placeholderClose))
	// placeholderLookalikeRe matches anything placeholder-shaped, well-formed
	// or not, so authored text can never impersonate a real attachment.
	placeholderLookalikeRe = regexp.MustCompile(regexp.QuoteMeta(placeholderOpen) + regexp.QuoteMeta(placeholderBody) +
		"[^" + regexp.QuoteMeta(placeholderClose) + "]*" + regexp.QuoteMeta(placeholderClose))
)

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
//
// A text run is scrubbed of placeholder-shaped substrings the document does not
// own, exactly as the plain-string form is: only the canonicalizer may mint a
// placeholder, so one document cannot cite another's image by writing the token
// itself.
func (c Content) Flatten() string {
	return c.flatten(nil)
}

// FlattenKeepingAttachments is Flatten for content this build assembled itself:
// an upload that minted a placeholder for an attachment the same request
// carried. The listed ids are exempt from the scrub, so the caller-authored
// text is still scrubbed while the minted reference survives.
func (c Content) FlattenKeepingAttachments(attachmentIDs []string) string {
	exempt := make(map[string]struct{}, len(attachmentIDs))
	for _, id := range attachmentIDs {
		if id != "" {
			exempt[id] = struct{}{}
		}
	}
	return c.flatten(exempt)
}

func (c Content) flatten(exempt map[string]struct{}) string {
	if !c.IsList {
		// A plain string carries no attachments of its own, so only the ids the
		// caller exempted survive; every other placeholder in it is authored
		// text.
		return scrubPlaceholders(c.Text, exempt)
	}
	// The exempt set is always non-nil: the blocks below mint their own
	// placeholders, and a nil map would scrub those along with the authored
	// ones.
	allowed := make(map[string]struct{}, len(c.Blocks)+len(exempt))
	for id := range exempt {
		allowed[id] = struct{}{}
	}
	for _, block := range c.Blocks {
		if block.Type != "image" && block.Type != "file" {
			continue
		}
		if id := block.attachmentShortID(); id != "" {
			allowed[id] = struct{}{}
		}
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
		body += scrubPlaceholders(block.Text, allowed)
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
	// Filename is the name the caller gave the block, empty when it named none.
	Filename string
	Data     []byte
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
			Filename:   block.Filename,
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
