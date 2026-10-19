package mcp

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// jsonFlavor selects how a payload is rendered as the text of a tool result.
// The reference renders every str-returning tool with CPython's json module,
// whose output differs per call site: the standard tools pretty-print with
// indent=2, an error payload is dumped compactly with the module's default
// separators (", " and ": "), and recall is dumped compactly with no spaces at
// all. The escaping is CPython's throughout — ensure_ascii leaves a non-ASCII
// character as \uXXXX — except where the caller asked for the raw form, as the
// SDK's compact dump does.
type jsonFlavor struct {
	indent int
	spaced bool
	ascii  bool
}

// prettyJSON is the flavor every standard tool result uses.
var prettyJSON = jsonFlavor{indent: 2, ascii: true}

// errorJSONFlavor is the flavor the reference's error payload uses: compact,
// with the module's default separators.
var errorJSONFlavor = jsonFlavor{spaced: true, ascii: true}

// jsonField is one member of an error payload, kept in the reference's order:
// the error sentence first, then the empty collection the tool adds beside it.
type jsonField struct {
	key   string
	value any
}

// renderJSON encodes v the way the flavor describes. The Go encoder supplies
// the structure (and the reference's key order, which the payload types fix by
// declaring their fields in the reference's order), and the two passes below
// give it CPython's escaping and separators.
func renderJSON(v any, flavor jsonFlavor) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// The reference's JSON does not HTML-escape, so "<" and "&" stay verbatim
	// instead of leaving as \u003c and \u0026.
	enc.SetEscapeHTML(false)
	if flavor.indent > 0 {
		enc.SetIndent("", strings.Repeat(" ", flavor.indent))
	}
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	if flavor.spaced {
		out = spaceSeparators(out)
	}
	if flavor.ascii {
		out = escapeNonASCII(out)
	}
	return string(out), nil
}

// pythonJSONText renders v as the text of a standard tool result: CPython's
// json.dumps(v, indent=2), which pretty-prints with ensure_ascii on.
func pythonJSONText(v any) string {
	text, err := renderJSON(v, prettyJSON)
	if err != nil {
		// A payload built from decoded values has nothing the encoder cannot
		// render; if one appears, the error sentence is still a valid answer.
		return err.Error()
	}
	return text
}

// compactToolText renders a payload the way a tool the reference registered
// with indent=None does: CPython's json.dumps with the module's default
// separators, so a success payload carries the same ", " and ": " the error
// payloads do.
func compactToolText(v any) toolText {
	text, err := renderJSON(v, errorJSONFlavor)
	if err != nil {
		return toolText(err.Error())
	}
	return toolText(text)
}

// errorText renders the reference's error payload: `{"error": <message>}`, with
// any extra member the tool includes beside it, dumped compactly with the
// module's default separators.
func errorText(message string, extras ...jsonField) string {
	var b strings.Builder
	b.WriteString(`{"error": `)
	b.WriteString(quotePythonJSON(message))
	for _, extra := range extras {
		b.WriteString(", ")
		b.WriteString(quotePythonJSON(extra.key))
		b.WriteString(": ")
		value, err := renderJSON(extra.value, errorJSONFlavor)
		if err != nil {
			value = "null"
		}
		b.WriteString(value)
	}
	b.WriteString("}")
	return b.String()
}

// quotePythonJSON renders one string as a JSON string literal with CPython's
// escaping, for the hand-built error payload.
func quotePythonJSON(value string) string {
	text, err := renderJSON(value, errorJSONFlavor)
	if err != nil {
		return strconv.Quote(value)
	}
	return text
}

// errorPayload is the answer every standard tool gives for a failure: a normal
// result whose text is the error payload, so the client reads it with the shape
// it expects rather than losing the session. The reference reports these as
// results (isError false), not as errors.
func errorPayload(message string, extras ...jsonField) toolText {
	return toolText(errorText(message, extras...))
}

// spaceSeparators rewrites a compact JSON document to CPython's default
// separators — ", " between members and ": " after a key — leaving string
// contents untouched.
func spaceSeparators(buf []byte) []byte {
	out := make([]byte, 0, len(buf)+len(buf)/8)
	inString := false
	for i := 0; i < len(buf); i++ {
		c := buf[i]
		out = append(out, c)
		if inString {
			switch c {
			case '\\':
				if i+1 < len(buf) {
					i++
					out = append(out, buf[i])
				}
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case ':', ',':
			out = append(out, ' ')
		}
	}
	return out
}

// escapeNonASCII rewrites every non-ASCII character of a JSON document as the
// \uXXXX escape CPython's ensure_ascii produces, a non-BMP character becoming
// the surrogate pair CPython emits. Only string literals can hold a non-ASCII
// byte in the encoder's output, so a byte-level pass cannot touch the syntax.
func escapeNonASCII(buf []byte) []byte {
	out := make([]byte, 0, len(buf))
	for i := 0; i < len(buf); {
		if buf[i] < utf8.RuneSelf {
			out = append(out, buf[i])
			i++
			continue
		}
		r, size := utf8.DecodeRune(buf[i:])
		if r == utf8.RuneError && size == 1 {
			out = append(out, buf[i])
			i++
			continue
		}
		out = appendUnicodeEscape(out, r)
		i += size
	}
	return out
}

// appendUnicodeEscape appends r as CPython's ensure_ascii renders it.
func appendUnicodeEscape(out []byte, r rune) []byte {
	if r > 0xFFFF {
		offset := r - 0x10000
		out = appendEscapeUnit(out, 0xD800+(offset>>10))
		return appendEscapeUnit(out, 0xDC00+(offset&0x3FF))
	}
	return appendEscapeUnit(out, r)
}

// appendEscapeUnit appends one \uXXXX escape.
func appendEscapeUnit(out []byte, unit rune) []byte {
	const hexDigits = "0123456789abcdef"
	return append(out, '\\', 'u',
		hexDigits[(unit>>12)&0xF], hexDigits[(unit>>8)&0xF],
		hexDigits[(unit>>4)&0xF], hexDigits[unit&0xF])
}
