package memoryops

// This file renders the pieces of the reference's validation report for the
// file route's `request` form part: the Python `repr` its `input_value=` field
// carries, the type name its `input_type=` field carries, and the wording its
// JSON decoder uses for a malformed document.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// fileRetainInputValue renders a refused value the way the model library's
// `input_value=` field does: Python's repr, with `None` for null.
func fileRetainInputValue(value any) string {
	var b strings.Builder
	writePythonRepr(&b, value)
	return b.String()
}

// writePythonRepr writes Python's repr of a decoded JSON value: single-quoted
// strings, `True`/`False`/`None`, and dicts that keep their key order.
func writePythonRepr(b *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		b.WriteString("None")
	case bool:
		if typed {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case string:
		b.WriteString(pythonStringRepr(typed))
	case json.Number:
		b.WriteString(typed.String())
	case []any:
		b.WriteByte('[')
		for index, element := range typed {
			if index > 0 {
				b.WriteString(", ")
			}
			writePythonRepr(b, element)
		}
		b.WriteByte(']')
	case orderedPairList:
		b.WriteByte('{')
		for index, pair := range typed {
			if index > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pythonStringRepr(pair.key))
			b.WriteString(": ")
			writePythonRepr(b, pair.value)
		}
		b.WriteByte('}')
	default:
		fmt.Fprintf(b, "%v", typed)
	}
}

// pythonStringRepr renders a Python string repr: single quotes unless the text
// contains one and not the other.
func pythonStringRepr(value string) string {
	quote := '\''
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case quote:
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// pythonTypeName names a decoded JSON value's Python type, which the report's
// `input_type=` field carries.
func pythonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case json.Number:
		return "int"
	case []any:
		return "list"
	case orderedPairList:
		return "dict"
	default:
		return "object"
	}
}

// pythonJSONMessage renders a JSON parse failure the way the library's decoder
// words it, from the position the Go decoder reports.
func pythonJSONMessage(raw string, err error) string {
	// A Go decoder reports its own wording; the library's is shorter and names
	// the line and column. The position is recovered from the offset.
	offset := 0
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		offset = int(syntax.Offset)
	}
	// The library's offset is one past the character it stopped at where Go's
	// is one past it, so the column it prints is one less.
	consumed := max(offset-1, 0)
	line := 1 + strings.Count(raw[:min(consumed, len(raw))], "\n")
	column := consumed - strings.LastIndex(raw[:min(consumed, len(raw))], "\n")
	// A document that ends mid-token is a different message, positioned at the
	// end of what arrived rather than where the decoder gave up.
	if strings.Contains(err.Error(), "unexpected end of JSON input") || strings.Contains(err.Error(), "unexpected EOF") {
		// The library's column is 1-based over the character it stopped at,
		// which is the last one for a truncated document.
		line = 1 + strings.Count(raw, "\n")
		column = len(raw) - strings.LastIndex(raw, "\n") - 1
		return fmt.Sprintf("EOF while parsing %s at line %d column %d", eofContext(raw), line, column)
	}
	switch {
	case strings.Contains(err.Error(), "looking for beginning of value"):
		return fmt.Sprintf("expected value at line %d column %d", line, column)
	case strings.Contains(err.Error(), "looking for beginning of object key string"):
		return fmt.Sprintf("key must be a string at line %d column %d", line, column)
	default:
		return fmt.Sprintf("%s at line %d column %d", strings.TrimPrefix(err.Error(), "invalid character "), line, column)
	}
}

// eofContext names the construct a truncated document was inside, which is the
// noun the library's EOF message uses: a string, an object or a value.
func eofContext(raw string) string {
	var stack []byte
	inString, escaped := false, false
	for _, r := range raw {
		if inString {
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, byte(r))
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	depth := len(stack)
	top := "a value"
	if depth > 0 {
		top = "an object"
		if stack[depth-1] == '[' {
			top = "a list"
		}
	}
	if inString {
		return "a string"
	}
	if depth > 0 {
		// A document that ends after a ':' or ',' is waiting for a value, not
		// for the object to close.
		trimmed := strings.TrimRight(raw, " \t\r\n")
		// A bareword is only a value being assembled at the top level: inside a
		// container the library has already committed to an object or array.
		// A document that ends on a bare literal (`tru`) is a value the reader
		// was still assembling, wherever it sits.
		if strings.HasSuffix(trimmed, ":") || strings.HasSuffix(trimmed, ",") ||
			(lastTokenIsBareword(trimmed) && !strings.HasSuffix(trimmed, "}") && !strings.HasSuffix(trimmed, "]")) {
			return "a value"
		}
		return top
	}
	return "a value"
}

// sortStrings sorts a string list in place, the way the reference's sorted()
// does for the misplaced per-file field names.
func sortStrings(values []string) { sort.Strings(values) }

// badRequestDetail wraps a sentence in the shared 400 envelope.
func badRequestDetail(detail string) *httpx.HTTPError {
	return httpx.NewHTTPError(httpx.KindBadRequest, detail)
}

// lastTokenIsBareword reports whether a truncated document ends mid-literal
// (`tru`, `fal`, `nul`) — a value the reader was still assembling. A bare digit
// ends a complete number, and `[`/`{` end no token at all, so neither counts.
func lastTokenIsBareword(raw string) bool {
	if raw == "" {
		return false
	}
	// Only a prefix of a literal is mid-token; a complete `true`/`false`/`null`
	// ends a value the reader finished.
	for _, complete := range []string{"true", "false", "null"} {
		if strings.HasSuffix(raw, complete) {
			return false
		}
	}
	last := raw[len(raw)-1]
	return (last >= 'a' && last <= 'z') || (last >= 'A' && last <= 'Z')
}

// remainingJSON returns the text after the first JSON document, empty when the
// reader consumed everything.
func remainingJSON(decoder *json.Decoder, raw string) string {
	offset := int(decoder.InputOffset())
	if offset >= len(raw) {
		return ""
	}
	return raw[offset:]
}

// orderedPairList is a JSON object whose member order is the caller's: the
// report's input_value renders objects the way the library's own decoder built
// them (insertion order), which a Go map cannot reproduce.
type orderedPairList []orderedPair

// orderedPair is one member of an orderedPairList.
type orderedPair struct {
	key   string
	value any
}

// decodeOrdered reads a JSON document keeping each object's member order. It
// returns a map-like value built from orderedPairList, and falls back to the
// plain decode for anything the token stream cannot describe.
func decodeOrdered(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	return decodeOrderedToken(decoder, token)
}

// decodeOrderedToken consumes the value that starts with token.
func decodeOrderedToken(decoder *json.Decoder, token json.Token) (any, error) {
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			pairs := orderedPairList{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyToken.(string)
				valueToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				value, err := decodeOrderedToken(decoder, valueToken)
				if err != nil {
					return nil, err
				}
				pairs = append(pairs, orderedPair{key: key, value: value})
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return pairs, nil
		case '[':
			list := []any{}
			for decoder.More() {
				valueToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				value, err := decodeOrderedToken(decoder, valueToken)
				if err != nil {
					return nil, err
				}
				list = append(list, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return list, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %v", typed)
	default:
		return typed, nil
	}
}

// orderedLookup reads one member of an ordered object.
func orderedLookup(object orderedPairList, member string) (any, bool) {
	for _, pair := range object {
		if pair.key == member {
			return pair.value, true
		}
	}
	return nil, false
}
