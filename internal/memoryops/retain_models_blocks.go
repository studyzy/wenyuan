package memoryops

// This file validates one content-block list against the reference's tagged
// union. The reference's framework validates every block in one pass and reports
// each rejected block at its own path, so a block its model refuses is recorded
// here rather than aborting the decode or being silently accepted.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

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
		if !hasTag {
			// No discriminator at all is the only case the framework reports as
			// a missing tag: any present value — null included — is stringified
			// and compared against the accepted set, and a value that matches
			// none is a tag mismatch rather than an absent tag.
			c.blockIssues = append(c.blockIssues, ContentBlockIssue{
				Loc:   []string{contentUnionPath, strconv.Itoa(index)},
				Kind:  "union_tag_not_found",
				Msg:   "Unable to extract tag using discriminator 'type'",
				Input: decodeAny(trimmed),
				Ctx:   stageCtx{Discriminator: "'type'"},
			})
			continue
		}
		if !isJSONStringValue(tagRaw) {
			// A discriminator that is not a string is stringified the way the
			// framework's own comparison renders it.
			tag = discriminatorText(trimSpaceBytes(tagRaw))
		}
		if tag != "text" && tag != "image" && tag != "file" {
			tags := "'" + strings.Join(contentBlockTagNames, "', '") + "'"
			c.blockIssues = append(c.blockIssues, ContentBlockIssue{
				Loc:   []string{contentUnionPath, strconv.Itoa(index)},
				Kind:  "union_tag_invalid",
				Msg:   "Input tag '" + tag + "' found using 'type' does not match any of the expected tags: " + tags,
				Input: decodeAny(trimmed),
				// The reference builds this context in the order it reports it:
				// the discriminator, the offending tag, then the accepted set.
				Ctx: stageCtx{Discriminator: "'type'", Tag: &tag, ExpectedTags: tags},
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
			Kind: "missing", Msg: "Field required", Input: sourceObjectValue(trimmedSource)})
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
			Kind: "missing", Msg: "Field required", Input: sourceObjectValue(trimmedSource)})
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

// sourceObjectValue is a block's source object as the caller wrote it, member
// order included: the reference reports a missing source member's input as the
// source object itself, and a decoded map would sort its keys.
func sourceObjectValue(raw []byte) any {
	if members, ok := orderedJSONObject(raw); ok {
		return members
	}
	return decodeAny(raw)
}
