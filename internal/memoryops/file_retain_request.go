package memoryops

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/validation"
)

// The `request` form part of the file route is a JSON *string* the reference
// validates with `FileRetainRequest.model_validate_json`
// (api/http.py:10404-10409). A failure there is not a framework validation
// item: the handler catches the exception and answers 400 with
// `f"Invalid request JSON: {e}"`, so the body is the model library's own
// multi-line report. This file reproduces that report member by member, because
// the exact wording, the order the members are reported in and the
// `[type=..., input_value=..., input_type=...]` suffix are all observable.

// fileRetainError is one entry of the report.
type fileRetainError struct {
	// loc is the member path, dot-separated and empty for the model itself.
	loc string
	// kind is the error type ("string_type", "list_type", "model_type", ...).
	kind string
	// msg is the message the library words for the error.
	msg string
	// input is the value the library carries; inputText overrides how it is
	// rendered when the value is not the raw JSON (a parse failure reports the
	// raw string).
	input any
	// inputType overrides the reported input type name.
	inputType string
	// inputText, when set, is the rendered input_value verbatim.
	inputText string
}

// fileRetainReport renders the library's report for FileRetainRequest, with the
// headline, the per-member blocks and the trailing document link line.
func fileRetainReport(problems []fileRetainError) *httpx.HTTPError {
	var b strings.Builder
	if len(problems) == 1 {
		b.WriteString("1 validation error for FileRetainRequest\n")
	} else {
		b.WriteString(strconv.Itoa(len(problems)) + " validation errors for FileRetainRequest\n")
	}
	for _, p := range problems {
		if p.loc != "" {
			b.WriteString(p.loc)
			b.WriteByte('\n')
		}
		inputText, inputType := p.inputText, p.inputType
		if inputText == "" {
			inputText = fileRetainInputValue(p.input)
			inputType = pythonTypeName(p.input)
		}
		if p.inputType != "" {
			inputType = p.inputType
		}
		fmt.Fprintf(&b, "  %s [type=%s, input_value=%s, input_type=%s]\n",
			p.msg, p.kind, inputText, inputType)
		fmt.Fprintf(&b, "    For further information visit https://errors.pydantic.dev/2.12/v/%s\n", p.kind)
	}
	return badRequestDetail("Invalid request JSON: " + strings.TrimSuffix(b.String(), "\n"))
}

// parseFileRetainRequest decodes the required `request` form field, reproducing
// the reference's own refusal when the model refuses it.
func (h *Handlers) parseFileRetainRequest(raw string) (parsedFileRetainRequest, *httpx.HTTPError) {
	// The document is validated with a plain decode first, so a malformed one is
	// reported from the decoder's own syntax error; only a well-formed document
	// is re-read keeping its member order.
	plain := json.NewDecoder(strings.NewReader(raw))
	plain.UseNumber()
	var probe any
	if err := plain.Decode(&probe); err != nil {
		// The library reports a malformed JSON document as a single json_invalid
		// error carrying the whole text.
		return parsedFileRetainRequest{}, fileRetainReport([]fileRetainError{{
			kind: "json_invalid", msg: "Invalid JSON: " + pythonJSONMessage(raw, err),
			input: raw, inputType: "str",
		}})
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	value, _ := decodeOrdered(decoder)
	// The library's reader consumes the whole string, so text after the
	// document is a trailing-characters failure rather than a value it ignores.
	if trailing := remainingJSON(decoder, raw); trailing != "" {
		column := len(raw) - len(trailing) + 1
		return parsedFileRetainRequest{}, fileRetainReport([]fileRetainError{{
			kind:  "json_invalid",
			msg:   fmt.Sprintf("Invalid JSON: trailing characters at line 1 column %d", column),
			input: raw, inputType: "str",
		}})
	}
	root, isObject := value.(orderedPairList)
	if !isObject {
		return parsedFileRetainRequest{}, fileRetainReport([]fileRetainError{{
			kind: "model_type", msg: "Input should be an object", input: value,
		}})
	}
	// The model validator runs before the members are checked (pydantic's
	// `mode="before"`), and raises one value_error carrying every placement
	// problem it found.
	problems := fileRetainPlacementErrors(root)
	parsed := parsedFileRetainRequest{parser: decodeFileParserSelectionAt(root, "parser", "parser", &problems)}
	metadataValue, _ := orderedLookup(root, "files_metadata")
	parsed.metadata = decodeFileMetadata(metadataValue, &problems)
	if len(problems) > 0 {
		return parsedFileRetainRequest{}, fileRetainReport(problems)
	}
	return parsed, nil
}

// fileRetainPlacementErrors is the model validator's report: per-file fields
// that belong in `files_metadata`, plus the update_mode the route does not
// support. Both are collected into one message, joined by "; ".
func fileRetainPlacementErrors(root orderedPairList) []fileRetainError {
	misplaced := make([]string, 0, len(fileRetainPerFileFields))
	for _, name := range fileRetainPerFileFields {
		if value, present := orderedLookup(root, name); present && value != nil {
			misplaced = append(misplaced, name)
		}
	}
	sortStrings(misplaced)
	problems := make([]string, 0, 2)
	if len(misplaced) > 0 {
		problems = append(problems, fmt.Sprintf(
			"Per-file fields (%s) must be placed in the corresponding 'files_metadata' entry",
			strings.Join(misplaced, ", ")))
	}
	if value, present := orderedLookup(root, "update_mode"); present && value != nil {
		problems = append(problems, "'update_mode' is not supported by /files/retain, which always processes asynchronously")
	}
	if len(problems) == 0 {
		return nil
	}
	return []fileRetainError{{kind: "value_error", msg: "Value error, " + strings.Join(problems, "; "), input: root}}
}

// decodeFileParserSelectionAt reads a `parser` member, which the model declares
// as `str | list[str]`. A value matching neither arm reports both arms'
// failures, in the order the union lists them. key is the member name the
// caller wrote; path is where the report files it.
func decodeFileParserSelectionAt(parent orderedPairList, key, path string, problems *[]fileRetainError) ParserSelection {
	value, present := orderedLookup(parent, key)
	if !present || value == nil {
		return nil
	}
	if text, ok := value.(string); ok {
		return ParserSelection{text}
	}
	if list, ok := value.([]any); ok {
		out := make(ParserSelection, 0, len(list))
		var elementProblems []fileRetainError
		for index, element := range list {
			text, ok := element.(string)
			if !ok {
				elementProblems = append(elementProblems, fileRetainError{
					loc: path + ".list[str]." + strconv.Itoa(index), kind: "string_type",
					msg: "Input should be a valid string", input: element,
				})
				continue
			}
			out = append(out, text)
		}
		if len(elementProblems) > 0 {
			// The union reports every arm's failure: the string arm refuses the
			// whole list, then the list arm names each element it cannot read.
			// A list whose every member is a string takes the list arm alone.
			*problems = append(*problems, fileRetainError{loc: path + ".str", kind: "string_type",
				msg: "Input should be a valid string", input: value})
			*problems = append(*problems, elementProblems...)
			return nil
		}
		return out
	}
	*problems = append(*problems,
		fileRetainError{loc: path + ".str", kind: "string_type",
			msg: "Input should be a valid string", input: value},
		fileRetainError{loc: path + ".list[str]", kind: "list_type",
			msg: "Input should be a valid array", input: value},
	)
	return nil
}

// fileRetainMetadataMembers are the FileRetainMetadata members in declaration
// order, each with the kind of check it needs.
var fileRetainMetadataMembers = []struct {
	name string
	kind string
}{
	{"document_id", "string"},
	{"context", "string"},
	{"metadata", "dict"},
	{"tags", "list"},
	{"timestamp", "string"},
	{"parser", "parser"},
	{"strategy", "string"},
}

// decodeFileMetadata reads the `files_metadata` member, which the model declares
// as `list[FileRetainMetadata] | None`. A non-list reports the list failure; a
// list reports each entry's own failures at its index, in member order.
func decodeFileMetadata(value any, problems *[]fileRetainError) []FileRetainMeta {
	if value == nil {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		*problems = append(*problems, fileRetainError{
			loc: "files_metadata", kind: "list_type", msg: "Input should be a valid array", input: value,
		})
		return nil
	}
	out := make([]FileRetainMeta, 0, len(list))
	for index, element := range list {
		prefix := "files_metadata." + strconv.Itoa(index)
		object, ok := element.(orderedPairList)
		if !ok {
			*problems = append(*problems, fileRetainError{
				loc: prefix, kind: "model_type", msg: "Input should be an object", input: element,
			})
			continue
		}
		out = append(out, decodeFileRetainMeta(object, prefix, problems))
	}
	return out
}

// decodeFileRetainMeta validates one entry member by member, reporting each
// failure at its own path. A member the entry leaves out keeps its default.
func decodeFileRetainMeta(object orderedPairList, prefix string, problems *[]fileRetainError) FileRetainMeta {
	meta := FileRetainMeta{}
	for _, member := range fileRetainMetadataMembers {
		value, present := orderedLookup(object, member.name)
		if !present || value == nil {
			continue
		}
		loc := prefix + "." + member.name
		switch member.kind {
		case "string":
			text, ok := value.(string)
			if !ok {
				*problems = append(*problems, fileRetainError{loc: loc, kind: "string_type",
					msg: "Input should be a valid string", input: value})
				continue
			}
			switch member.name {
			case "document_id":
				meta.DocumentID = text
			case "context":
				meta.Context = text
			case "timestamp":
				meta.Timestamp = text
				// The reference's field validator runs after the type check and
				// refuses a year-1/9999 value at request time; a value it cannot
				// parse is returned as-is, so a format error keeps surfacing
				// where it always has (api/http.py FileRetainMetadata).
				if parsed, err := parseISODate(text); err == nil && validation.IsPlaceholderDate(parsed) {
					*problems = append(*problems, fileRetainError{loc: loc, kind: "value_error",
						msg: fmt.Sprintf("Value error, timestamp year %d is a placeholder, not a date.",
							parsed.Year()),
						input: text})
				}
			case "strategy":
				meta.Strategy = text
			}
		case "dict":
			dict, ok := value.(orderedPairList)
			if !ok {
				*problems = append(*problems, fileRetainError{loc: loc, kind: "dict_type",
					msg: "Input should be an object", input: value})
				continue
			}
			members := make(map[string]any, len(dict))
			for _, pair := range dict {
				members[pair.key] = pair.value
			}
			meta.Metadata = members
		case "list":
			list, ok := value.([]any)
			if !ok {
				*problems = append(*problems, fileRetainError{loc: loc, kind: "list_type",
					msg: "Input should be a valid array", input: value})
				continue
			}
			tags := make([]string, 0, len(list))
			for tagIndex, element := range list {
				tag, ok := element.(string)
				if !ok {
					*problems = append(*problems, fileRetainError{
						loc: loc + "." + strconv.Itoa(tagIndex), kind: "string_type",
						msg: "Input should be a valid string", input: element,
					})
					continue
				}
				tags = append(tags, tag)
			}
			meta.Tags = tags
		case "parser":
			meta.Parser = decodeFileParserSelectionAt(object, member.name, loc, problems)
		}
	}
	return meta
}
