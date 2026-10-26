package memoryops

// This file renders and validates a retain item's content once its blocks are
// decoded: what counts as carrying content, what the canonical body is (with
// attachments as placeholders in position), and how a placeholder the document
// does not own is scrubbed.

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

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

// FlattenDecliningAttachments is Flatten for content the deployment's model
// cannot read images from. The reference canonicalizes such content by dropping
// every attachment block and keeping the text, with the block's position left as
// a paragraph break, so the document reads as the prose the model will see rather
// than as a placeholder nothing can resolve.
func (c Content) FlattenDecliningAttachments(attachmentIDs []string) string {
	if !c.IsList {
		return c.flatten(nil)
	}
	exempt := make(map[string]struct{}, len(attachmentIDs))
	for _, id := range attachmentIDs {
		if id != "" {
			exempt[id] = struct{}{}
		}
	}
	body := ""
	afterAttachment := false
	for _, block := range c.Blocks {
		if block.Type == "image" || block.Type == "file" {
			afterAttachment = true
			continue
		}
		if afterAttachment {
			body = padToBlankLine(body)
			afterAttachment = false
		}
		body += scrubPlaceholders(block.Text, exempt)
	}
	return body
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

// stripPlaceholders removes every placeholder-shaped substring from text, which
// is what a text-only model reading an attachment-bearing document gets: the
// prose, with no reference to a picture it will never be shown.
func stripPlaceholders(text string) string {
	return scrubPlaceholders(text, nil)
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
