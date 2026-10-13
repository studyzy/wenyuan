package openai

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/studyzy/wenyuan/internal/provider"
)

// scaffoldTags are the reasoning or scaffolding tag names stripped from
// free-form content. A paired and an unpaired form are both handled. The
// vocabulary is deliberately small and documented rather than exhaustive:
// the spec requires tags to be stripped but does not enumerate them.
var scaffoldTags = []string{"think", "thinking", "reasoning", "scratchpad", "analysis"}

// StripScaffolding removes reasoning and scaffolding tags and their contents
// from free-form content, leaving the answer text.
func StripScaffolding(content string) string {
	out := content
	for _, tag := range scaffoldTags {
		out = stripPairedTag(out, tag)
		out = stripUnpairedTag(out, tag)
	}
	return strings.TrimSpace(out)
}

// stripPairedTag removes every <tag>...</tag> region, including the tags and
// the content between them.
func stripPairedTag(content, tag string) string {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	for {
		start := strings.Index(content, open)
		if start < 0 {
			return content
		}
		end := strings.Index(content[start+len(open):], close)
		if end < 0 {
			return content
		}
		content = content[:start] + content[start+len(open)+end+len(close):]
	}
}

// stripUnpairedTag removes lone opening, closing, or self-closing tag markers
// whose partner never appeared.
func stripUnpairedTag(content, tag string) string {
	for _, marker := range []string{"<" + tag + ">", "</" + tag + ">", "<" + tag + "/>"} {
		content = strings.ReplaceAll(content, marker, "")
	}
	return content
}

// ParseStructured parses structured model output into a value. Content is
// unwrapped from a markdown code fence when present, and raw control
// characters inside string values are escaped rather than blanked so
// multi-line content survives. Validation against the schema is applied
// unless skipValidation is set.
func ParseStructured(content string, schema *provider.Schema, skipValidation bool) (any, error) {
	cleaned := extractJSON(content)
	if cleaned == "" {
		return nil, &provider.MalformedResponseError{Detail: "structured output carried no JSON value"}
	}
	var raw any
	if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
		repaired, repairErr := repairControlCharacters(cleaned)
		if repairErr != nil {
			return nil, &provider.MalformedResponseError{Detail: "structured output is not valid JSON", Cause: err}
		}
		if err := json.Unmarshal([]byte(repaired), &raw); err != nil {
			return nil, &provider.MalformedResponseError{Detail: "structured output is not valid JSON", Cause: err}
		}
	}
	if skipValidation || schema == nil {
		return raw, nil
	}
	if err := ValidateValue(raw, schema); err != nil {
		return raw, err
	}
	return raw, nil
}

// extractJSON unwraps markdown code fences and trims surrounding prose so the
// first balanced JSON value is isolated.
func extractJSON(content string) string {
	trimmed := strings.TrimSpace(content)
	if fenced, ok := unwrapFence(trimmed); ok {
		trimmed = fenced
	}
	if value, ok := isolateJSONValue(trimmed); ok {
		return value
	}
	return trimmed
}

// unwrapFence removes a surrounding ``` fence, with or without a language tag.
func unwrapFence(content string) (string, bool) {
	if !strings.HasPrefix(content, "```") {
		return content, false
	}
	body := content[3:]
	if newline := strings.IndexByte(body, '\n'); newline >= 0 {
		language := strings.TrimSpace(body[:newline])
		if language == "" || isLanguageTag(language) {
			body = body[newline+1:]
		}
	}
	if end := strings.LastIndex(body, "```"); end >= 0 {
		body = body[:end]
	}
	return strings.TrimSpace(body), true
}

// isLanguageTag reports whether a fence info string looks like a language
// identifier rather than JSON content.
func isLanguageTag(info string) bool {
	if info == "" {
		return false
	}
	for _, r := range info {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// isolateJSONValue returns the first balanced object or array in the text.
func isolateJSONValue(content string) (string, bool) {
	start := strings.IndexAny(content, "{[")
	if start < 0 {
		return "", false
	}
	open := content[start]
	var close byte = '}'
	if open == '[' {
		close = ']'
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(content); i++ {
		ch := content[i]
		switch {
		case escaped:
			escaped = false
		case ch == '\\' && inString:
			escaped = true
		case ch == '"':
			inString = !inString
		case inString:
			// control characters inside a string are handled by repair
		case ch == open:
			depth++
		case ch == close:
			depth--
			if depth == 0 {
				return content[start : i+1], true
			}
		}
	}
	return "", false
}

// repairControlCharacters escapes raw control characters that appear inside
// JSON string values, so multi-line content survives parsing instead of being
// rejected or blanked.
func repairControlCharacters(content string) (string, error) {
	var out strings.Builder
	out.Grow(len(content))
	inString := false
	escaped := false
	for i := 0; i < len(content); i++ {
		ch := content[i]
		switch {
		case escaped:
			out.WriteByte(ch)
			escaped = false
		case ch == '\\' && inString:
			out.WriteByte(ch)
			escaped = true
		case ch == '"':
			out.WriteByte(ch)
			inString = !inString
		case inString && ch < 0x20:
			out.WriteString(escapeControl(ch))
		default:
			out.WriteByte(ch)
		}
	}
	return out.String(), nil
}

// escapeControl renders a raw control character as its JSON escape sequence.
func escapeControl(ch byte) string {
	switch ch {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	default:
		const hex = "0123456789abcdef"
		return `\u00` + string([]byte{hex[ch>>4], hex[ch&0x0f]})
	}
}
