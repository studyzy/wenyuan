package reporting

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// dryRunContentUnionLoc is the loc segment the framework files a content-block
// list under when it validates the list arm of the `str | list[blocks]` union.
// It names the union's members the way the framework's generated schema does.
const dryRunContentUnionLoc = "list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]"

// dryRunContentTags are the block discriminators the union accepts, in the order
// the framework lists them in a refusal.
const dryRunContentTags = "text, image, file"

// dryRunMediaTypeRe mirrors the reference's MIME-shape check on an attachment
// source's media type: a `type/subtype` with the punctuation MIME types use.
var dryRunMediaTypeRe = regexp.MustCompile(`^[\w.+-]+/[\w.+-]+$`)

// dryRunContentFailure validates a dry-run request's `content` value the way the
// reference's request model does, before any extraction runs.
//
// It returns either a structured 422 validation body (a structural failure: a
// missing or unrecognisable block discriminator, a blank value, a wrong type) or
// a 400 sentence (a canonicalization failure: an attachment payload that is not
// decodable base64 or is empty). Exactly one is non-nil; both nil means the
// content is valid.
//
// raw is the content field's raw JSON: nil when the field was absent, the
// literal `null` when it was sent as JSON null.
func dryRunContentFailure(raw json.RawMessage) (*httpx.ValidationFailure, string) {
	if raw == nil {
		return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Missing([]string{"body", "content"}, map[string]any{}),
		}}, ""
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		// The body decoded as JSON already, so the content value is valid JSON;
		// a failure here cannot happen in practice. Report it as a bad body
		// rather than pretend the content was fine.
		return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.ValidationItem("json_invalid", []string{"body", "content"}, "JSON decode error", nil, nil),
		}}, ""
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return contentEmptyFailure(typed), ""
		}
		return nil, ""
	case []any:
		return dryRunBlockListFailure(typed)
	default:
		// A JSON null, number, boolean or object matches neither union arm, so
		// the framework reports both arms' type failures.
		return unionTypeFailure(value, "list_type", "Input should be a valid list"), ""
	}
}

// contentEmptyFailure is the framework's field-validator rejection of a blank
// content value: the item carries the rejected value verbatim, so a caller sees
// exactly what it sent.
func contentEmptyFailure(input any) *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.ValueError([]string{"body", "content"}, input, "content cannot be empty"),
	}}
}

// unionTypeFailure renders the two type failures the `str | list[blocks]` union
// reports for a value that matches neither arm: the string arm's type failure
// and the list arm's.
func unionTypeFailure(input any, listKind, listMsg string) *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.ValidationItem("string_type", []string{"body", "content", "str"},
			"Input should be a valid string", input, nil),
		httpx.ValidationItem(listKind, []string{"body", "content", dryRunContentUnionLoc}, listMsg, input, nil),
	}}
}

// dryRunBlockListFailure validates a content-block list. It reports the block
// failures first (the string arm's type failure followed by each rejected
// block), then the blank-content rejection, and only once the list is
// structurally sound checks the attachment payloads for canonicalization
// failures, which the reference reports as 400.
func dryRunBlockListFailure(list []any) (*httpx.ValidationFailure, string) {
	var blockErrors []httpx.ValidationError
	for index, element := range list {
		blockErrors = append(blockErrors, dryRunBlockErrors(index, element)...)
	}
	if len(blockErrors) > 0 {
		head := httpx.ValidationItem("string_type", []string{"body", "content", "str"},
			"Input should be a valid string", list, nil)
		return &httpx.ValidationFailure{Errors: append([]httpx.ValidationError{head}, blockErrors...)}, ""
	}
	// A list with no attachment and no non-blank text carries no content, which
	// the framework's own field validator rejects. An attachment-only list is
	// valid multimodal input, so only the both-absent case is blank.
	if !dryRunListCarriesContent(list) {
		return contentEmptyFailure(list), ""
	}
	for index, element := range list {
		block, ok := element.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := block["type"].(string)
		if tag != "image" && tag != "file" {
			continue
		}
		source, ok := block["source"].(map[string]any)
		if !ok {
			continue
		}
		data, _ := source["data"].(string)
		if data == "" {
			return nil, "content[" + strconv.Itoa(index) + "]: attachment source data is empty"
		}
		if _, err := base64.StdEncoding.DecodeString(data); err != nil {
			return nil, "content[" + strconv.Itoa(index) +
				"]: attachment source data is not valid base64: Only base64 data is allowed"
		}
	}
	return nil, ""
}

// dryRunListCarriesContent reports whether a block list carries anything to
// extract: an attachment block, or a text block with non-blank text.
func dryRunListCarriesContent(list []any) bool {
	for _, element := range list {
		block, ok := element.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "image", "file":
			return true
		case "text":
			if text, ok := block["text"].(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
	}
	return false
}

// dryRunBlockErrors validates one content block against the discriminated union,
// naming each failure at the loc the framework files it under.
func dryRunBlockErrors(index int, element any) []httpx.ValidationError {
	prefix := []string{"body", "content", dryRunContentUnionLoc, strconv.Itoa(index)}
	block, ok := element.(map[string]any)
	if !ok {
		return []httpx.ValidationError{httpx.ValidationItem("model_attributes_type", prefix,
			"Input should be a valid dictionary or object to extract fields from", element, nil)}
	}
	tag, hasTag := block["type"].(string)
	if !hasTag {
		return []httpx.ValidationError{httpx.ValidationItem("union_tag_not_found", prefix,
			"Unable to extract tag using discriminator 'type'", element,
			map[string]any{"discriminator": "'type'"})}
	}
	switch tag {
	case "text":
		return dryRunTextFieldErrors(prefix, block)
	case "image", "file":
		return dryRunAttachmentErrors(prefix, tag, block)
	default:
		return []httpx.ValidationError{httpx.ValidationItem("union_tag_invalid", prefix,
			"Input tag '"+tag+"' found using 'type' does not match any of the expected tags: '"+
				strings.ReplaceAll(dryRunContentTags, ", ", "', '")+"'",
			element, map[string]any{
				"discriminator": "'type'",
				"tag":           tag,
				"expected_tags": "'" + strings.ReplaceAll(dryRunContentTags, ", ", "', '") + "'",
			})}
	}
}

// dryRunTextFieldErrors validates a text block's single required field.
func dryRunTextFieldErrors(prefix []string, block map[string]any) []httpx.ValidationError {
	value, present := block["text"]
	if !present {
		return []httpx.ValidationError{httpx.Missing(append(append([]string{}, prefix...), "text", "text"), block)}
	}
	if _, ok := value.(string); !ok {
		return []httpx.ValidationError{httpx.ValidationItem("string_type",
			append(append([]string{}, prefix...), "text", "text"), "Input should be a valid string", value, nil)}
	}
	return nil
}

// dryRunAttachmentErrors validates an image or file block's source envelope: the
// source object, its `base64` discriminator, the MIME-shaped media type and the
// data string. Each failure is named at the field the framework names it at.
func dryRunAttachmentErrors(prefix []string, tag string, block map[string]any) []httpx.ValidationError {
	sourceLoc := append(append([]string{}, prefix...), tag, "source")
	rawSource, present := block["source"]
	if !present {
		return []httpx.ValidationError{httpx.Missing(sourceLoc, block)}
	}
	source, ok := rawSource.(map[string]any)
	if !ok {
		return []httpx.ValidationError{httpx.ValidationItem("model_attributes_type", sourceLoc,
			"Input should be a valid dictionary or object to extract fields from", rawSource, nil)}
	}
	var errors []httpx.ValidationError
	if rawType, present := source["type"]; present {
		if value, ok := rawType.(string); !ok || value != "base64" {
			errors = append(errors, httpx.ValidationItem("literal_error", append(append([]string{}, sourceLoc...), "type"),
				"Input should be 'base64'", rawType, map[string]any{"expected": "'base64'"}))
		}
	}
	if rawMedia, present := source["media_type"]; !present {
		errors = append(errors, httpx.Missing(append(append([]string{}, sourceLoc...), "media_type"), source))
	} else if media, ok := rawMedia.(string); !ok {
		errors = append(errors, httpx.ValidationItem("string_type",
			append(append([]string{}, sourceLoc...), "media_type"), "Input should be a valid string", rawMedia, nil))
	} else if !dryRunMediaTypeRe.MatchString(media) {
		errors = append(errors, httpx.ValueError(append(append([]string{}, sourceLoc...), "media_type"), rawMedia,
			"media_type must look like 'type/subtype', got '"+media+"'"))
	}
	if rawData, present := source["data"]; !present {
		errors = append(errors, httpx.Missing(append(append([]string{}, sourceLoc...), "data"), source))
	} else if _, ok := rawData.(string); !ok {
		errors = append(errors, httpx.ValidationItem("string_type",
			append(append([]string{}, sourceLoc...), "data"), "Input should be a valid string", rawData, nil))
	}
	return errors
}
